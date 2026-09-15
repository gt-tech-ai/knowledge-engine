"""Async retry decorator for transient errors using tenacity.

The async analog of ``retry.retry_transient`` (which wraps a *sync* callable and cannot
await a coroutine). Retries coroutines that raise transient ``AppError``s (TIMEOUT,
UNAVAILABLE); permanent errors are re-raised without retry.
"""

from __future__ import annotations

import functools
from collections.abc import Awaitable, Callable
from typing import TypeVar, cast

from tenacity import (
    retry,
    retry_if_exception,
    stop_after_attempt,
    wait_exponential_jitter,
)

from techai_webutils.core.errors.errors import AppError

F = TypeVar("F", bound=Callable[..., Awaitable[object]])


def _is_transient(err: BaseException) -> bool:
    """Return True only for ``AppError`` instances whose ``is_transient`` flag is set."""
    return isinstance(err, AppError) and err.is_transient


def retry_transient_async(
    max_attempts: int = 3,
    base_delay: float = 0.1,
    max_delay: float = 10.0,
    sleep: Callable[[float], Awaitable[None]] | None = None,
) -> Callable[[F], F]:
    """Return a decorator that retries a coroutine on transient ``AppError``s.

    ``max_attempts`` bounds the total attempts (including the first try); ``base_delay``
    and ``max_delay`` (seconds) bound the exponential-with-jitter backoff. Permanent
    errors are re-raised without retry. ``sleep`` (optional) overrides the coroutine used
    to wait between attempts — inject a deterministic recorder in tests / callers (e.g. the
    KB-sync watchdog) that need to control or record the backoff schedule; the default is
    tenacity's normal async sleep, so the behavior is unchanged when it is omitted.
    """

    def decorator(func: F) -> F:
        """Attach the tenacity async retry policy to *func*, preserving its metadata."""
        retry_cond = retry_if_exception(_is_transient)
        stop_cond = stop_after_attempt(max_attempts)
        wait_pol = wait_exponential_jitter(initial=base_delay, max=max_delay, jitter=base_delay)
        # Only forward ``sleep`` when provided so the default (tenacity's async sleep) is preserved. The
        # tenacity stub types ``sleep`` as a sync callable, but an async sleep is valid for a coroutine
        # function (AsyncRetrying awaits it) — hence the boundary ignore on that one branch.
        retrying = (
            retry(retry=retry_cond, stop=stop_cond, wait=wait_pol, reraise=True, sleep=sleep)  # ty: ignore[invalid-argument-type]  # pyright: ignore[reportArgumentType]
            if sleep is not None
            else retry(retry=retry_cond, stop=stop_cond, wait=wait_pol, reraise=True)
        )

        @functools.wraps(func)
        async def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped coroutine under the configured retry policy."""
            return await func(*args, **kwargs)

        return cast("F", retrying(wrapper))

    return decorator
