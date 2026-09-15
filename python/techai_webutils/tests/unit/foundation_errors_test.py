"""Unit tests for foundation error extensions.

This file tests structured error context, error classification (transient vs
permanent), gRPC/HTTP status code mapping, and error chaining via IngestionErrors
and MultiError.

# Test Coverage

The tests cover:
  - Error classification: transient, permanent, internal, and unknown categories
  - gRPC mapping: application errors to correct gRPC status codes
  - HTTP mapping: application errors to correct HTTP status codes
  - IngestionErrors: ExceptionGroup collection and except* compatibility
  - MultiError: heterogeneous error aggregation with human-readable summary

# Test Structure

Tests use pytest class-based organization grouped by function (classification,
gRPC mapping, HTTP mapping, IngestionErrors, MultiError). No mocking is needed
since these are pure utility functions operating on error instances.

# Running Tests

Run with: pytest tests/python/test_foundation/test_errors.py
"""

from techai_webutils.core.errors.errors import (
    IngestionError,
    InternalError,
    NotFoundError,
    AppTimeoutError,
    UnavailableError,
)
from techai_webutils.foundation.errors.errors import (
    IngestionErrors,
    MultiError,
    classify_error,
    to_grpc_status,
    to_http_status,
)


class TestErrorClassification:
    """Test suite for classify_error categorization function."""

    def test_timeout_is_transient(self) -> None:
        """Test that classify_error returns 'transient' for AppTimeoutError.

        **Why this test is important:**
          - Retry logic uses this classification to decide whether to retry
          - Timeout errors indicate temporary network or service latency
          - Misclassification would prevent retries on recoverable failures

        **What it tests:**
          - classify_error(AppTimeoutError) returns "transient"
        """
        err = AppTimeoutError("timed out")
        assert classify_error(err) == "transient"

    def test_unavailable_is_transient(self) -> None:
        """Test that classify_error returns 'transient' for UnavailableError.

        **Why this test is important:**
          - Unavailable services typically recover within seconds
          - Retry with backoff is the correct strategy for this class of error
          - Circuit breakers depend on accurate transient classification

        **What it tests:**
          - classify_error(UnavailableError) returns "transient"
        """
        err = UnavailableError("down")
        assert classify_error(err) == "transient"

    def test_not_found_is_permanent(self) -> None:
        """Test that classify_error returns 'permanent' for NotFoundError.

        **Why this test is important:**
          - Retrying a NOT_FOUND error will never succeed without caller action
          - Permanent classification prevents wasteful retry attempts
          - Metrics systems use this to distinguish client errors from infrastructure issues

        **What it tests:**
          - classify_error(NotFoundError) returns "permanent"
        """
        err = NotFoundError("gone")
        assert classify_error(err) == "permanent"

    def test_internal_is_internal(self) -> None:
        """Test that classify_error returns 'internal' for InternalError.

        **Why this test is important:**
          - Internal errors represent bugs that require developer investigation
          - Alerting systems trigger on internal classifications for on-call escalation
          - Distinguishing internal from transient prevents false retry attempts

        **What it tests:**
          - classify_error(InternalError) returns "internal"
        """
        err = InternalError("bug")
        assert classify_error(err) == "internal"

    def test_non_app_error_is_unknown(self) -> None:
        """Test that classify_error returns 'unknown' for non-AppError exceptions.

        **Why this test is important:**
          - Third-party library exceptions must not be silently swallowed
          - Unknown classification triggers conservative error handling (no retry, alert)
          - Ensures the system is defensive against unexpected exception types

        **What it tests:**
          - classify_error(ValueError) returns "unknown"
        """
        assert classify_error(ValueError("bad")) == "unknown"


class TestGRPCMapping:
    """Test suite for to_grpc_status mapping function."""

    def test_not_found_maps_to_5(self) -> None:
        """Test that to_grpc_status returns 5 (NOT_FOUND) for NotFoundError.

        **Why this test is important:**
          - gRPC clients use status codes to render appropriate user-facing messages
          - Status 5 triggers "resource not found" handling in generated client stubs
          - Must match the Go server's status code for cross-language consistency

        **What it tests:**
          - to_grpc_status(NotFoundError) returns 5
        """
        err = NotFoundError("missing")
        assert to_grpc_status(err) == 5

    def test_timeout_maps_to_4(self) -> None:
        """Test that to_grpc_status returns 4 (DEADLINE_EXCEEDED) for AppTimeoutError.

        **Why this test is important:**
          - gRPC DEADLINE_EXCEEDED signals the client to potentially retry with longer timeout
          - Incorrect mapping (e.g., UNKNOWN) would prevent client-side retry logic
          - Must align with Go gateway's deadline propagation behavior

        **What it tests:**
          - to_grpc_status(AppTimeoutError) returns 4
        """
        err = AppTimeoutError()
        assert to_grpc_status(err) == 4

    def test_unavailable_maps_to_14(self) -> None:
        """Test that to_grpc_status returns 14 (UNAVAILABLE) for UnavailableError.

        **Why this test is important:**
          - gRPC UNAVAILABLE tells load balancers to retry on a different backend
          - Client-side retry policies are configured per-status-code
          - Must match the Go server behavior for transparent failover

        **What it tests:**
          - to_grpc_status(UnavailableError) returns 14
        """
        err = UnavailableError()
        assert to_grpc_status(err) == 14

    def test_non_app_error_maps_to_2(self) -> None:
        """Test that to_grpc_status returns 2 (UNKNOWN) for non-AppError exceptions.

        **Why this test is important:**
          - Unrecognized errors must not leak internal details to clients
          - UNKNOWN status triggers generic error handling in client code
          - Defensive default prevents information disclosure vulnerabilities

        **What it tests:**
          - to_grpc_status(RuntimeError) returns 2
        """
        assert to_grpc_status(RuntimeError("oops")) == 2


class TestHTTPMapping:
    """Test suite for to_http_status mapping function."""

    def test_not_found_maps_to_404(self) -> None:
        """Test that to_http_status returns 404 for NotFoundError.

        **Why this test is important:**
          - REST clients use HTTP 404 to distinguish missing resources from server errors
          - Frontend UI renders specific "not found" pages based on this status
          - Search engines use 404 to de-index removed pages

        **What it tests:**
          - to_http_status(NotFoundError) returns 404
        """
        err = NotFoundError("missing")
        assert to_http_status(err) == 404

    def test_internal_maps_to_500(self) -> None:
        """Test that to_http_status returns 500 for InternalError.

        **Why this test is important:**
          - HTTP 500 triggers alerting and monitoring escalation
          - Clients display generic error messages on 500 responses
          - SLA calculations exclude 4xx but include 5xx error rates

        **What it tests:**
          - to_http_status(InternalError) returns 500
        """
        err = InternalError()
        assert to_http_status(err) == 500


class TestIngestionErrors:
    """Test suite for IngestionErrors ExceptionGroup."""

    def test_collects_multiple_errors(self) -> None:
        """Test that IngestionErrors aggregates multiple IngestionError instances.

        **Why this test is important:**
          - Batch ingestion processes many documents; individual failures must not halt the batch
          - Aggregated errors enable partial success reporting with per-document failure details
          - Downstream retry logic uses the error list to identify failed documents

        **What it tests:**
          - group.exceptions contains exactly 2 errors
        """
        errors = [
            IngestionError("parse failed", document_id="doc1", stage="parse"),
            IngestionError("embed failed", document_id="doc2", stage="embed"),
        ]
        group = IngestionErrors("batch failed", errors)
        assert len(group.exceptions) == 2

    def test_except_star_compatible(self) -> None:
        """Test that IngestionErrors is catchable with Python 3.11+ except* syntax.

        **Why this test is important:**
          - except* enables granular handling of individual errors within a group
          - Batch processing callers can retry specific document failures
          - Python 3.11+ ExceptionGroup compatibility is required for modern error handling

        **What it tests:**
          - IngestionErrors raised and caught with except* IngestionError succeeds
        """
        errors = [IngestionError("fail", document_id="d1")]
        group = IngestionErrors("batch", errors)
        caught = False
        try:
            raise group
        except* IngestionError:
            caught = True
        assert caught


class TestMultiError:
    """Test suite for MultiError aggregation."""

    def test_multi_error_collects(self) -> None:
        """Test that MultiError stores errors and provides a readable summary.

        **Why this test is important:**
          - Operations that contact multiple services may accumulate heterogeneous errors
          - A single MultiError simplifies error propagation through the call stack
          - The string representation aids debugging in logs and error reports

        **What it tests:**
          - multi.errors contains exactly 2 errors
          - str(multi) includes "2 errors" for human readability
        """
        errs = [NotFoundError("a"), AppTimeoutError("b")]
        multi = MultiError(errs)
        assert len(multi.errors) == 2
        assert "2 errors" in str(multi)
