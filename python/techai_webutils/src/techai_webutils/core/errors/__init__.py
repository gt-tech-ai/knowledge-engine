"""Domain error types for the Tech AI Knowledge Engine."""

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
    QuotaExceededError,
    UnauthorizedError,
    UnavailableError,
)

__all__ = [
    "AppError",
    "AppTimeoutError",
    "ConflictError",
    "ErrorCode",
    "ForbiddenError",
    "IngestionError",
    "InternalError",
    "InvalidInputError",
    "NotFoundError",
    "QuotaExceededError",
    "UnauthorizedError",
    "UnavailableError",
]
