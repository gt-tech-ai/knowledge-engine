"""Unit tests for Python core error taxonomy.

This file tests that error codes, gRPC/HTTP status mappings, transient/permanent
classification, and convenience error constructors all match the Go implementation.
Cross-language parity is critical -- mismatched codes cause silent routing failures.

# Test Coverage

The tests cover:
  - AppError construction: code, message, details, and cause chaining
  - gRPC status mapping: all error codes map to correct gRPC status integers
  - HTTP status mapping: all error codes map to correct HTTP status integers
  - Transient classification: TIMEOUT and UNAVAILABLE are retryable
  - Permanent classification: NOT_FOUND, INVALID_INPUT, etc. are non-retryable
  - Convenience constructors: NotFoundError, InvalidInputError, etc.
  - ErrorCode string values: must match Go constants exactly

# Test Structure

Tests use pytest class-based organization grouped by concern (AppError behavior,
convenience constructors, ErrorCode values). No mocking is needed since these
are pure domain objects.

# Running Tests

Run with: pytest tests/python/core/test_errors.py
"""

from techai_webutils.core.errors.errors import (
    AppError,
    ConflictError,
    ErrorCode,
    ForbiddenError,
    IngestionError,
    InternalError,
    InvalidInputError,
    NotFoundError,
    AppTimeoutError,
    UnauthorizedError,
    UnavailableError,
)


class TestAppError:
    """Test suite for AppError structured error type."""

    def test_create_with_code_and_message(self):
        """Test that AppError stores code and message correctly.

        **Why this test is important:**
          - AppError is the base error type for all cross-service error propagation
          - Incorrect code/message storage would break error handling in every service
          - str() representation is used in log output and user-facing messages

        **What it tests:**
          - err.code equals the provided ErrorCode
          - err.message equals the provided string
          - str(err) returns the message
        """
        err = AppError(ErrorCode.NOT_FOUND, "user not found")
        assert err.code == ErrorCode.NOT_FOUND
        assert err.message == "user not found"
        assert str(err) == "user not found"

    def test_grpc_status_mapping(self):
        """Test that gRPC status codes match the Go implementation.

        **Why this test is important:**
          - Python and Go services communicate over gRPC; mismatched codes cause incorrect error handling
          - gRPC clients use status codes to decide retry, fallback, or user-facing error behavior
          - These mappings are defined by the gRPC specification and must be exact

        **What it tests:**
          - NOT_FOUND maps to gRPC 5
          - INVALID_INPUT maps to gRPC 3
          - UNAUTHORIZED maps to gRPC 16
          - FORBIDDEN maps to gRPC 7
          - CONFLICT maps to gRPC 6
          - INTERNAL maps to gRPC 13
          - TIMEOUT maps to gRPC 4
          - UNAVAILABLE maps to gRPC 14
        """
        mapping = {
            ErrorCode.NOT_FOUND: 5,
            ErrorCode.INVALID_INPUT: 3,
            ErrorCode.UNAUTHORIZED: 16,
            ErrorCode.FORBIDDEN: 7,
            ErrorCode.CONFLICT: 6,
            ErrorCode.INTERNAL: 13,
            ErrorCode.TIMEOUT: 4,
            ErrorCode.UNAVAILABLE: 14,
        }
        for code, expected_grpc in mapping.items():
            err = AppError(code, "test")
            assert err.grpc_status == expected_grpc, (
                f"{code}: expected gRPC {expected_grpc}, got {err.grpc_status}"
            )

    def test_http_status_mapping(self):
        """Test that HTTP status codes match the Go implementation.

        **Why this test is important:**
          - REST API responses use these codes; incorrect mapping produces confusing client errors
          - HTTP status codes drive client retry logic and error UI rendering
          - Must stay synchronized with Go gateway responses for consistent API behavior

        **What it tests:**
          - NOT_FOUND maps to HTTP 404
          - INVALID_INPUT maps to HTTP 400
          - UNAUTHORIZED maps to HTTP 401
          - FORBIDDEN maps to HTTP 403
          - CONFLICT maps to HTTP 409
          - INTERNAL maps to HTTP 500
          - TIMEOUT maps to HTTP 504
          - UNAVAILABLE maps to HTTP 503
        """
        mapping = {
            ErrorCode.NOT_FOUND: 404,
            ErrorCode.INVALID_INPUT: 400,
            ErrorCode.UNAUTHORIZED: 401,
            ErrorCode.FORBIDDEN: 403,
            ErrorCode.CONFLICT: 409,
            ErrorCode.INTERNAL: 500,
            ErrorCode.TIMEOUT: 504,
            ErrorCode.UNAVAILABLE: 503,
        }
        for code, expected_http in mapping.items():
            err = AppError(code, "test")
            assert err.http_status == expected_http, (
                f"{code}: expected HTTP {expected_http}, got {err.http_status}"
            )

    def test_is_transient(self):
        """Test that transient errors are correctly identified as retryable.

        **Why this test is important:**
          - Retry logic depends on this classification to avoid wasting resources on permanent failures
          - Misclassifying a permanent error as transient causes infinite retry loops
          - Misclassifying a transient error as permanent causes premature failure

        **What it tests:**
          - TIMEOUT is classified as transient (True)
          - UNAVAILABLE is classified as transient (True)
          - NOT_FOUND is classified as non-transient (False)
          - INTERNAL is classified as non-transient (False)
        """
        assert AppError(ErrorCode.TIMEOUT, "t").is_transient is True
        assert AppError(ErrorCode.UNAVAILABLE, "u").is_transient is True
        assert AppError(ErrorCode.NOT_FOUND, "n").is_transient is False
        assert AppError(ErrorCode.INTERNAL, "i").is_transient is False

    def test_is_permanent(self):
        """Test that permanent errors are correctly identified as non-retryable.

        **Why this test is important:**
          - Retrying permanent errors wastes compute and delays user-facing error responses
          - Circuit breakers use this classification to decide whether to count a failure
          - Incorrect classification can trigger circuit breaker trips on client errors

        **What it tests:**
          - NOT_FOUND is classified as permanent (True)
          - INVALID_INPUT is classified as permanent (True)
          - UNAUTHORIZED is classified as permanent (True)
          - FORBIDDEN is classified as permanent (True)
          - CONFLICT is classified as permanent (True)
          - TIMEOUT is classified as non-permanent (False)
        """
        assert AppError(ErrorCode.NOT_FOUND, "n").is_permanent is True
        assert AppError(ErrorCode.INVALID_INPUT, "i").is_permanent is True
        assert AppError(ErrorCode.UNAUTHORIZED, "u").is_permanent is True
        assert AppError(ErrorCode.FORBIDDEN, "f").is_permanent is True
        assert AppError(ErrorCode.CONFLICT, "c").is_permanent is True
        assert AppError(ErrorCode.TIMEOUT, "t").is_permanent is False

    def test_details(self):
        """Test that AppError carries arbitrary metadata in its details dict.

        **Why this test is important:**
          - Error details provide context for debugging and structured error responses
          - API clients depend on details for field-level validation feedback
          - Observability systems extract details for error aggregation and alerting

        **What it tests:**
          - err.details contains the key-value pairs passed at construction
        """
        err = AppError(ErrorCode.NOT_FOUND, "not found", details={"id": "123"})
        assert err.details == {"id": "123"}

    def test_cause(self):
        """Test that AppError chains underlying exceptions for debugging.

        **Why this test is important:**
          - Exception chaining preserves the root cause for debugging production incidents
          - Without cause chaining, internal errors lose their origin stack trace
          - Observability tools use the cause to group related errors

        **What it tests:**
          - err.cause references the original exception passed at construction
        """
        cause = ValueError("bad value")
        err = AppError(ErrorCode.INTERNAL, "failed", cause=cause)
        assert err.cause is cause


class TestConvenienceErrors:
    """Test suite for convenience error constructors."""

    def test_not_found(self):
        """Test that NotFoundError uses NOT_FOUND code and passes kwargs as details.

        **Why this test is important:**
          - NotFoundError is the most common error in CRUD operations
          - Details kwargs allow callers to attach resource identifiers for debugging
          - Incorrect code assignment would map to wrong HTTP/gRPC status

        **What it tests:**
          - err.code is ErrorCode.NOT_FOUND
          - err.details contains the keyword arguments passed at construction
        """
        err = NotFoundError("user not found", user_id="123")
        assert err.code == ErrorCode.NOT_FOUND
        assert err.details == {"user_id": "123"}

    def test_invalid_input(self):
        """Test that InvalidInputError uses INVALID_INPUT code with field details.

        **Why this test is important:**
          - Input validation errors must carry field context for client-side form feedback
          - API consumers depend on the field name to highlight the correct input
          - Consistent code assignment ensures proper 400 HTTP responses

        **What it tests:**
          - err.code is ErrorCode.INVALID_INPUT
          - err.details contains the field name
        """
        err = InvalidInputError("bad email", field="email")
        assert err.code == ErrorCode.INVALID_INPUT
        assert err.details == {"field": "email"}

    def test_unauthorized(self):
        """Test that UnauthorizedError defaults to correct code and message.

        **Why this test is important:**
          - Authentication failures must produce consistent 401 responses
          - Default message avoids leaking internal details to unauthenticated callers
          - Security middleware depends on this error type to trigger re-authentication

        **What it tests:**
          - err.code is ErrorCode.UNAUTHORIZED
          - err.message is "authentication required"
        """
        err = UnauthorizedError()
        assert err.code == ErrorCode.UNAUTHORIZED
        assert err.message == "authentication required"

    def test_forbidden(self):
        """Test that ForbiddenError uses FORBIDDEN code.

        **Why this test is important:**
          - Authorization failures must be distinguishable from authentication failures
          - RBAC enforcement depends on this error to indicate insufficient permissions
          - Incorrect code would produce 401 instead of 403, confusing clients

        **What it tests:**
          - err.code is ErrorCode.FORBIDDEN
        """
        err = ForbiddenError()
        assert err.code == ErrorCode.FORBIDDEN

    def test_conflict(self):
        """Test that ConflictError uses CONFLICT code.

        **Why this test is important:**
          - Conflict errors indicate duplicate resource creation or stale updates
          - Clients use 409 responses to trigger conflict resolution workflows
          - Optimistic concurrency control depends on this error type

        **What it tests:**
          - err.code is ErrorCode.CONFLICT
        """
        err = ConflictError("duplicate", slug="test")
        assert err.code == ErrorCode.CONFLICT

    def test_internal(self):
        """Test that InternalError uses INTERNAL code and chains the cause.

        **Why this test is important:**
          - Internal errors represent unexpected failures requiring developer investigation
          - Cause chaining ensures the root exception is preserved for debugging
          - 500 responses trigger alerting in monitoring systems

        **What it tests:**
          - err.code is ErrorCode.INTERNAL
          - err.cause references the original exception
        """
        cause = RuntimeError("boom")
        err = InternalError(cause=cause)
        assert err.code == ErrorCode.INTERNAL
        assert err.cause is cause

    def test_timeout(self):
        """Test that AppTimeoutError uses TIMEOUT code.

        **Why this test is important:**
          - Timeout errors are transient and must be retryable
          - Correct code assignment ensures circuit breakers count timeouts properly
          - 504 HTTP responses signal gateway timeouts to clients

        **What it tests:**
          - err.code is ErrorCode.TIMEOUT
        """
        err = AppTimeoutError()
        assert err.code == ErrorCode.TIMEOUT

    def test_unavailable(self):
        """Test that UnavailableError uses UNAVAILABLE code.

        **Why this test is important:**
          - Unavailable errors indicate temporary service outages
          - Retry logic and circuit breakers depend on this classification
          - 503 HTTP responses allow clients to implement backoff strategies

        **What it tests:**
          - err.code is ErrorCode.UNAVAILABLE
        """
        err = UnavailableError()
        assert err.code == ErrorCode.UNAVAILABLE

    def test_ingestion_error(self):
        """Test that IngestionError uses INTERNAL code and includes document context.

        **Why this test is important:**
          - Ingestion errors must carry document_id and stage for pipeline debugging
          - Failed documents need to be identified for retry or dead-letter processing
          - Stage information enables pinpointing which pipeline step failed

        **What it tests:**
          - err.code is ErrorCode.INTERNAL
          - err.details contains document_id
          - err.details contains stage
        """
        err = IngestionError("parse failed", document_id="doc-1", stage="parsing")
        assert err.code == ErrorCode.INTERNAL
        assert err.details["document_id"] == "doc-1"
        assert err.details["stage"] == "parsing"


class TestErrorCodeValues:
    """Test suite for ErrorCode string value parity with Go constants."""

    def test_code_values(self):
        """Test that all ErrorCode string values match Go-compatible uppercase constants.

        **Why this test is important:**
          - Error codes are serialized over the wire between Python and Go services
          - Any mismatch causes silent failures in cross-service error propagation
          - These values are used in gRPC metadata and SQS message attributes

        **What it tests:**
          - NOT_FOUND.value equals "NOT_FOUND"
          - INVALID_INPUT.value equals "INVALID_INPUT"
          - UNAUTHORIZED.value equals "UNAUTHORIZED"
          - FORBIDDEN.value equals "FORBIDDEN"
          - CONFLICT.value equals "CONFLICT"
          - INTERNAL.value equals "INTERNAL"
          - TIMEOUT.value equals "TIMEOUT"
          - UNAVAILABLE.value equals "UNAVAILABLE"
        """
        assert ErrorCode.NOT_FOUND.value == "NOT_FOUND"
        assert ErrorCode.INVALID_INPUT.value == "INVALID_INPUT"
        assert ErrorCode.UNAUTHORIZED.value == "UNAUTHORIZED"
        assert ErrorCode.FORBIDDEN.value == "FORBIDDEN"
        assert ErrorCode.CONFLICT.value == "CONFLICT"
        assert ErrorCode.INTERNAL.value == "INTERNAL"
        assert ErrorCode.TIMEOUT.value == "TIMEOUT"
        assert ErrorCode.UNAVAILABLE.value == "UNAVAILABLE"
