"""Tests for domain error to gRPC status mapper."""

from __future__ import annotations

from techai_webutils.clients.rpc.grpc.errors import to_grpc_status
from techai_webutils.core.errors.errors import (
    ConflictError,
    ForbiddenError,
    InternalError,
    InvalidInputError,
    NotFoundError,
    AppTimeoutError,
    UnauthorizedError,
    UnavailableError,
)
import grpc
from unittest.mock import MagicMock


class TestToGrpcStatus:
    """Test suite for to_grpc_status error mapping."""

    def test_not_found(self) -> None:
        """Test that a NotFoundError maps to gRPC NOT_FOUND with its message preserved.

        **Why this test is important:**
          - Clients branch on the gRPC status code to drive UX (e.g. show a 404 page vs retry)
          - A wrong code would mislead callers; a dropped message would hide actionable detail
          - NotFound is client-safe, so its message must pass through unredacted

        **What it tests:**
          - The status code is NOT_FOUND
          - The original "user not found" message is returned verbatim
        """
        code, msg = to_grpc_status(NotFoundError("user not found"))
        assert code == grpc.StatusCode.NOT_FOUND
        assert msg == "user not found"

    def test_invalid_input(self) -> None:
        """Test that an InvalidInputError maps to INVALID_ARGUMENT with its message preserved.

        **Why this test is important:**
          - Validation failures must reach the client as INVALID_ARGUMENT so they aren't retried as transient
          - The specific message ("bad email") is what tells the user how to fix their request
          - Confirms client-facing validation errors are surfaced, not hidden

        **What it tests:**
          - The status code is INVALID_ARGUMENT
          - The original "bad email" message is returned verbatim
        """
        code, msg = to_grpc_status(InvalidInputError("bad email"))
        assert code == grpc.StatusCode.INVALID_ARGUMENT
        assert msg == "bad email"

    def test_conflict(self) -> None:
        """Test that a ConflictError maps to ALREADY_EXISTS with its message preserved.

        **Why this test is important:**
          - Duplicate-resource conflicts must be distinguishable from generic failures by callers
          - ALREADY_EXISTS lets clients handle idempotency/uniqueness collisions correctly
          - The message conveys which resource collided, so it must not be redacted

        **What it tests:**
          - The status code is ALREADY_EXISTS
          - The original "duplicate" message is returned verbatim
        """
        code, msg = to_grpc_status(ConflictError("duplicate"))
        assert code == grpc.StatusCode.ALREADY_EXISTS
        assert msg == "duplicate"

    def test_unauthorized(self) -> None:
        """Test that an UnauthorizedError maps to gRPC UNAUTHENTICATED.

        **Why this test is important:**
          - Missing/invalid credentials must signal UNAUTHENTICATED so clients trigger re-auth
          - Confusing this with PERMISSION_DENIED would send users down the wrong recovery path
          - Correct auth-status mapping is core to the security contract

        **What it tests:**
          - The status code is UNAUTHENTICATED
        """
        code, _msg = to_grpc_status(UnauthorizedError())
        assert code == grpc.StatusCode.UNAUTHENTICATED

    def test_forbidden(self) -> None:
        """Test that a ForbiddenError maps to gRPC PERMISSION_DENIED.

        **Why this test is important:**
          - An authenticated-but-unauthorized request must report PERMISSION_DENIED, not UNAUTHENTICATED
          - Mapping it to a retryable or auth code would mishandle a genuine access-control denial
          - Correct authorization-status mapping protects the access-control model

        **What it tests:**
          - The status code is PERMISSION_DENIED
        """
        code, _msg = to_grpc_status(ForbiddenError())
        assert code == grpc.StatusCode.PERMISSION_DENIED

    def test_timeout(self) -> None:
        """Test that an AppTimeoutError maps to gRPC DEADLINE_EXCEEDED.

        **Why this test is important:**
          - Timeouts are transient, so clients/interceptors retry on DEADLINE_EXCEEDED
          - Mapping a timeout to a fatal code would prevent legitimate retries and hurt reliability
          - Confirms the transient classification is encoded in the wire status

        **What it tests:**
          - The status code is DEADLINE_EXCEEDED
        """
        code, _msg = to_grpc_status(AppTimeoutError())
        assert code == grpc.StatusCode.DEADLINE_EXCEEDED

    def test_unavailable(self) -> None:
        """Test that an UnavailableError maps to gRPC UNAVAILABLE.

        **Why this test is important:**
          - UNAVAILABLE is the canonical transient code that retry logic keys off of
          - A wrong mapping would either suppress retries or retry non-retryable failures
          - Confirms downstream-outage signaling reaches the caller correctly

        **What it tests:**
          - The status code is UNAVAILABLE
        """
        code, _msg = to_grpc_status(UnavailableError())
        assert code == grpc.StatusCode.UNAVAILABLE

    def test_internal_hides_message(self) -> None:
        """Test that an InternalError reports a generic message while logging the real one.

        **Why this test is important:**
          - Leaking internal error text to clients risks exposing secrets or implementation detail
          - Operators still need the real cause, so it must be logged rather than discarded
          - This redact-but-log behavior is the security boundary for server-side failures

        **What it tests:**
          - The status code is INTERNAL
          - The client-facing message is the generic "internal error" (real detail redacted)
          - The original "secret details" text is passed to the logger
        """
        logger = MagicMock()
        code, msg = to_grpc_status(InternalError("secret details"), logger)  # type: ignore[arg-type]
        assert code == grpc.StatusCode.INTERNAL
        assert msg == "internal error"
        # Should have logged the real error
        assert any("secret details" in str(c) for c in logger.mock_calls)

    def test_unknown_exception_maps_to_internal(self) -> None:
        """Test that an unrecognized exception is treated as an internal error.

        **Why this test is important:**
          - Any non-AppError (e.g. a bare RuntimeError) must fail closed as INTERNAL, never leak through
          - The unhandled cause must still be logged so operators can diagnose the unexpected failure
          - Confirms the catch-all branch protects clients from raw, unexpected error text

        **What it tests:**
          - A RuntimeError maps to status INTERNAL with the generic "internal error" message
          - The original "crash" text is passed to the logger
        """
        logger = MagicMock()
        code, msg = to_grpc_status(RuntimeError("crash"), logger)  # type: ignore[arg-type]
        assert code == grpc.StatusCode.INTERNAL
        assert msg == "internal error"
        assert any("crash" in str(c) for c in logger.mock_calls)

    def test_without_logger(self) -> None:
        """Test that mapping works when no logger is supplied.

        **Why this test is important:**
          - The logger argument is optional; call sites without one must not raise
          - The redaction guarantee must hold even when there is nowhere to log the cause
          - Confirms the mapper degrades gracefully rather than crashing on a missing dependency

        **What it tests:**
          - A RuntimeError with no logger still maps to INTERNAL with the generic "internal error" message
        """
        code, msg = to_grpc_status(RuntimeError("crash"))
        assert code == grpc.StatusCode.INTERNAL
        assert msg == "internal error"
