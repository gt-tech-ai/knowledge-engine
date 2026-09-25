"""Structured error taxonomy matching the Go error package.

Error codes map to identical gRPC status codes in both Go and Python,
ensuring consistent error handling across both languages.
"""

from enum import StrEnum


class ErrorCode(StrEnum):
    """Error codes shared between Go and Python services."""

    NOT_FOUND = "NOT_FOUND"
    """Requested resource does not exist (HTTP 404 / gRPC NOT_FOUND); terminal, not retryable."""
    INVALID_INPUT = "INVALID_INPUT"
    """Request failed validation (HTTP 400 / gRPC INVALID_ARGUMENT); terminal, not retryable."""
    UNAUTHORIZED = "UNAUTHORIZED"
    """Authentication missing or token invalid (HTTP 401 / gRPC UNAUTHENTICATED); terminal."""
    FORBIDDEN = "FORBIDDEN"
    """Authenticated but lacking permission for the action (HTTP 403 / gRPC PERMISSION_DENIED)."""
    CONFLICT = "CONFLICT"
    """Resource already exists or version mismatch (HTTP 409 / gRPC ALREADY_EXISTS); terminal."""
    INTERNAL = "INTERNAL"
    """Unexpected server-side failure (HTTP 500 / gRPC INTERNAL); terminal, not retryable."""
    TIMEOUT = "TIMEOUT"
    """Operation exceeded its deadline (HTTP 504 / gRPC DEADLINE_EXCEEDED); transient, retryable."""
    UNAVAILABLE = "UNAVAILABLE"
    """Dependency temporarily unreachable (HTTP 503 / gRPC UNAVAILABLE); transient, retryable."""


# gRPC status code mapping (matches Go's ToGRPCStatus)
_GRPC_STATUS_MAP: dict[ErrorCode, int] = {
    ErrorCode.NOT_FOUND: 5,  # NOT_FOUND
    ErrorCode.INVALID_INPUT: 3,  # INVALID_ARGUMENT
    ErrorCode.UNAUTHORIZED: 16,  # UNAUTHENTICATED
    ErrorCode.FORBIDDEN: 7,  # PERMISSION_DENIED
    ErrorCode.CONFLICT: 6,  # ALREADY_EXISTS
    ErrorCode.INTERNAL: 13,  # INTERNAL
    ErrorCode.TIMEOUT: 4,  # DEADLINE_EXCEEDED
    ErrorCode.UNAVAILABLE: 14,  # UNAVAILABLE
}
"""Maps each ErrorCode to its canonical gRPC status code (matches Go's ToGRPCStatus)."""

# HTTP status code mapping (matches Go's ToHTTPStatus)
_HTTP_STATUS_MAP: dict[ErrorCode, int] = {
    ErrorCode.NOT_FOUND: 404,
    ErrorCode.INVALID_INPUT: 400,
    ErrorCode.UNAUTHORIZED: 401,
    ErrorCode.FORBIDDEN: 403,
    ErrorCode.CONFLICT: 409,
    ErrorCode.INTERNAL: 500,
    ErrorCode.TIMEOUT: 504,
    ErrorCode.UNAVAILABLE: 503,
}
"""Maps each ErrorCode to its canonical HTTP status code (matches Go's ToHTTPStatus)."""


class AppError(Exception):
    """Base application error with structured error code and details."""

    def __init__(
        self,
        code: ErrorCode,
        message: str,
        details: dict[str, str] | None = None,
        cause: Exception | None = None,
    ) -> None:
        """Initialize the error with a code, message, optional details and cause."""
        super().__init__(message)
        self.code = code
        self.message = message
        self.details = details or {}
        self.cause = cause

    @property
    def grpc_status(self) -> int:
        """Return the corresponding gRPC status code."""
        return _GRPC_STATUS_MAP.get(self.code, 2)  # UNKNOWN

    @property
    def http_status(self) -> int:
        """Return the corresponding HTTP status code."""
        return _HTTP_STATUS_MAP.get(self.code, 500)

    @property
    def is_transient(self) -> bool:
        """Return True if this error may succeed on retry."""
        return self.code in (ErrorCode.TIMEOUT, ErrorCode.UNAVAILABLE)

    @property
    def is_permanent(self) -> bool:
        """Return True if this error will not succeed on retry."""
        return self.code in (
            ErrorCode.NOT_FOUND,
            ErrorCode.INVALID_INPUT,
            ErrorCode.UNAUTHORIZED,
            ErrorCode.FORBIDDEN,
            ErrorCode.CONFLICT,
        )

    def __repr__(self) -> str:
        """Return an unambiguous representation including the code and message."""
        return f"AppError(code={self.code.value}, message={self.message!r})"


# Concrete error subclasses for convenience


class NotFoundError(AppError):
    """Resource not found."""

    def __init__(self, message: str, **kwargs: str) -> None:
        """Initialize with a message; extra keyword args become error details."""
        super().__init__(ErrorCode.NOT_FOUND, message, details=kwargs)


class InvalidInputError(AppError):
    """Invalid input or validation failure."""

    def __init__(self, message: str, **kwargs: str) -> None:
        """Initialize with a message; extra keyword args become error details."""
        super().__init__(ErrorCode.INVALID_INPUT, message, details=kwargs)


class UnauthorizedError(AppError):
    """Authentication required or token invalid."""

    def __init__(self, message: str = "authentication required") -> None:
        """Initialize with an optional message describing the auth failure."""
        super().__init__(ErrorCode.UNAUTHORIZED, message)


class ForbiddenError(AppError):
    """Insufficient permissions."""

    def __init__(self, message: str = "insufficient permissions") -> None:
        """Initialize with an optional message describing the denied action."""
        super().__init__(ErrorCode.FORBIDDEN, message)


class ConflictError(AppError):
    """Resource conflict (duplicate, version mismatch, etc.)."""

    def __init__(self, message: str, **kwargs: str) -> None:
        """Initialize with a message; extra keyword args become error details."""
        super().__init__(ErrorCode.CONFLICT, message, details=kwargs)


class InternalError(AppError):
    """Internal server error."""

    def __init__(self, message: str = "internal error", cause: Exception | None = None) -> None:
        """Initialize with an optional message and underlying cause exception."""
        super().__init__(ErrorCode.INTERNAL, message, cause=cause)


class AppTimeoutError(AppError):
    """Operation timed out."""

    def __init__(self, message: str = "operation timed out") -> None:
        """Initialize with an optional message describing the timed-out operation."""
        super().__init__(ErrorCode.TIMEOUT, message)


class UnavailableError(AppError):
    """Service unavailable."""

    def __init__(self, message: str = "service unavailable") -> None:
        """Initialize with an optional message describing the unavailable service."""
        super().__init__(ErrorCode.UNAVAILABLE, message)


# Ingestion-specific errors


class IngestionError(AppError):
    """Error during document ingestion."""

    def __init__(
        self,
        message: str,
        document_id: str | None = None,
        stage: str | None = None,
        cause: Exception | None = None,
    ) -> None:
        """Initialize with a message and optional document id, stage and cause.

        The ``document_id`` and ``stage`` values, when provided, are recorded
        in the error details to identify which document and pipeline stage
        failed.
        """
        details: dict[str, str] = {}
        if document_id:
            details["document_id"] = document_id
        if stage:
            details["stage"] = stage
        super().__init__(ErrorCode.INTERNAL, message, details=details, cause=cause)
