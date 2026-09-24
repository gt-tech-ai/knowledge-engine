"""Shared gRPC client-boundary error translation (ARCHITECTURE.md#error-codes).

Maps a raw ``grpc.aio.AioRpcError`` raised by an InternalService stub call into a coded ``AppError``
at the client boundary, plus the ``wrap_grpc_errors`` decorator that applies it to an async client
method. Every InternalService client (document-status, ingestion-job-state, …) uses this so a
transient status surfaces as a transient ``AppError`` the composition-root resiliency stack
(RetryProxy) can retry — instead of a raw SDK error escaping the retry loop (the KB-sync UNAVAILABLE
incident). Extracted here (a Layer-1 foundation module every client imports downward) so the two
InternalService clients share ONE boundary rather than a lateral clients→clients import.
"""

from __future__ import annotations

import functools
from typing import TYPE_CHECKING

import grpc
from grpc.aio import AioRpcError

from techai_webutils.core.errors import AppError, ErrorCode, InternalError

if TYPE_CHECKING:
    from collections.abc import Callable
    from types import CoroutineType
    from typing import Any

# gRPC status codes treated as transient/retryable at the client boundary (ARCHITECTURE.md#error-codes).
#
# NOTE — this set is DELIBERATELY different from ``classify._grpc_transient`` (which lists only
# UNAVAILABLE + DEADLINE_EXCEEDED): the boundary additionally treats RESOURCE_EXHAUSTED as transient
# for the ``AppError`` it raises, so a rate-limited InternalService call is retried by the resiliency
# stack. ``classify.is_transient`` is the generic exception classifier (used for DLQ routing across
# many families); this is the InternalService-call boundary mapper. Editing one does NOT imply
# editing the other — the divergence is intentional.
_TRANSIENT_GRPC_CODES = frozenset(
    {
        grpc.StatusCode.UNAVAILABLE,
        grpc.StatusCode.DEADLINE_EXCEEDED,
        grpc.StatusCode.RESOURCE_EXHAUSTED,
    }
)
"""gRPC status codes mapped to a transient (retryable) ``AppError`` at the InternalService boundary."""


def grpc_error_to_app_error(exc: AioRpcError) -> AppError:
    """Map a raw gRPC ``AioRpcError`` to a coded ``AppError`` at the client boundary (ARCHITECTURE.md#error-codes).

    A transient status (UNAVAILABLE / DEADLINE_EXCEEDED / RESOURCE_EXHAUSTED) becomes an ``AppError``
    with a transient ``ErrorCode`` — so the resiliency stack (RetryProxy, which only retries a
    transient ``AppError``) can absorb a brief endpoint flap — while every other status becomes a
    terminal ``INTERNAL`` error carrying the gRPC cause. Without this, the SDK error escaped the retry
    loop on the first attempt (the KB-sync ``UNAVAILABLE`` incident).
    """
    status = exc.code()
    detail = exc.details() or str(exc)
    if status in _TRANSIENT_GRPC_CODES:
        code = ErrorCode.TIMEOUT if status is grpc.StatusCode.DEADLINE_EXCEEDED else ErrorCode.UNAVAILABLE
        return AppError(code, f"api InternalService {status.name}: {detail}", cause=exc)
    return InternalError(f"api InternalService {status.name}: {detail}", cause=exc)


def wrap_grpc_errors[**P, R](
    fn: Callable[P, CoroutineType[Any, Any, R]],
) -> Callable[P, CoroutineType[Any, Any, R]]:
    """Translate a raw gRPC ``AioRpcError`` at an async client-method boundary into a coded ``AppError``.

    Wrap every InternalService stub call so a transient status surfaces as a transient ``AppError``
    the resiliency stack can classify + retry, instead of a raw SDK error that escapes the retry loop.
    The ``ParamSpec`` preserves the method's exact signature so the decorated client still satisfies
    its port Protocol.
    """

    @functools.wraps(fn)
    async def wrapper(*args: P.args, **kwargs: P.kwargs) -> R:
        try:
            return await fn(*args, **kwargs)
        except AioRpcError as exc:
            raise grpc_error_to_app_error(exc) from exc

    return wrapper
