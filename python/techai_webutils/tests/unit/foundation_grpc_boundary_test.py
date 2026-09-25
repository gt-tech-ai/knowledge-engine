"""Tests for the shared gRPC client-boundary error translation (foundation/resilience/grpc_boundary)."""

from __future__ import annotations

import grpc
import pytest
from grpc.aio import AioRpcError, Metadata

from techai_webutils.core.errors import AppError
from techai_webutils.foundation.resilience.grpc_boundary import (
    grpc_error_to_app_error,
    wrap_grpc_errors,
)


def _rpc_error(code: grpc.StatusCode, detail: str = "boom") -> AioRpcError:
    """Build a raw AioRpcError for a status code (matching the SDK's positional constructor)."""
    return AioRpcError(code, Metadata(), Metadata(), detail)


class TestGrpcErrorToAppError:
    def test_transient_codes_map_to_transient_app_error(self) -> None:
        """Test that UNAVAILABLE / DEADLINE_EXCEEDED / RESOURCE_EXHAUSTED become transient AppErrors.

        **Why this test is important:**
          - RetryProxy only retries a transient ``AppError``; a raw ``AioRpcError`` escapes the retry
            loop on the first attempt. The boundary must classify these as transient so every gRPC
            client wrapped with it gets a retryable error.

        **What it tests:**
          - Each transient gRPC status yields an ``AppError`` with ``is_transient`` True (never the raw error).
        """
        for code in (
            grpc.StatusCode.UNAVAILABLE,
            grpc.StatusCode.DEADLINE_EXCEEDED,
            grpc.StatusCode.RESOURCE_EXHAUSTED,
        ):
            err = grpc_error_to_app_error(_rpc_error(code))
            assert isinstance(err, AppError)
            assert err.is_transient
            assert not isinstance(err, AioRpcError)

    def test_terminal_code_maps_to_non_transient_app_error(self) -> None:
        """Test that a terminal status (NOT_FOUND) becomes a non-transient AppError.

        **Why this test is important:**
          - A terminal gRPC status must NOT be retried; surfacing it as a non-transient ``AppError``
            lets the caller fail fast instead of the resiliency stack looping on a permanent error.

        **What it tests:**
          - NOT_FOUND yields an ``AppError`` with ``is_transient`` False.
        """
        err = grpc_error_to_app_error(_rpc_error(grpc.StatusCode.NOT_FOUND))
        assert isinstance(err, AppError)
        assert not err.is_transient


class TestWrapGrpcErrors:
    @pytest.mark.asyncio
    async def test_wraps_raw_rpc_error_into_app_error(self) -> None:
        """Test that the decorator translates a raised AioRpcError into a coded AppError.

        **Why this test is important:**
          - Client methods raise raw ``AioRpcError`` from the stub; the decorator is what turns that
            into the coded ``AppError`` the resiliency stack understands — the shared mechanism every
            wrapped gRPC client relies on.

        **What it tests:**
          - A decorated coroutine raising ``AioRpcError(UNAVAILABLE)`` instead raises a transient
            ``AppError`` (not the raw SDK error).
        """

        @wrap_grpc_errors
        async def call() -> None:
            raise _rpc_error(grpc.StatusCode.UNAVAILABLE)

        with pytest.raises(AppError) as excinfo:
            await call()
        assert excinfo.value.is_transient
        assert not isinstance(excinfo.value, AioRpcError)
