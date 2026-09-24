"""Domain error to gRPC status code mapper.

Mirrors Go's ``go/transport/rpc/errors.go`` ``ToConnectError()``.
Maps ``AppError`` codes to gRPC status codes using client-safe messages.
"""

from __future__ import annotations

from techai_webutils.core.errors.errors import AppError, ErrorCode
import grpc
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.logger import Logger

_CODE_MAP: dict[ErrorCode, grpc.StatusCode] = {
    ErrorCode.NOT_FOUND: grpc.StatusCode.NOT_FOUND,
    ErrorCode.INVALID_INPUT: grpc.StatusCode.INVALID_ARGUMENT,
    ErrorCode.CONFLICT: grpc.StatusCode.ALREADY_EXISTS,
    ErrorCode.UNAUTHORIZED: grpc.StatusCode.UNAUTHENTICATED,
    ErrorCode.FORBIDDEN: grpc.StatusCode.PERMISSION_DENIED,
    ErrorCode.TIMEOUT: grpc.StatusCode.DEADLINE_EXCEEDED,
    ErrorCode.UNAVAILABLE: grpc.StatusCode.UNAVAILABLE,
    ErrorCode.INTERNAL: grpc.StatusCode.INTERNAL,
}
"""Maps each domain ``ErrorCode`` to the gRPC status code returned to clients."""


def to_grpc_status(err: Exception, logger: Logger | None = None) -> tuple[grpc.StatusCode, str]:
    """Map an exception to a gRPC status code and client-safe message.

    Args:
        err: The exception to map.
        logger: Optional logger for recording internal errors.

    Returns:
        Tuple of (grpc.StatusCode, message).

    """
    if isinstance(err, AppError):
        code = _CODE_MAP.get(err.code, grpc.StatusCode.INTERNAL)
        # For internal errors, hide the real message from clients
        if code == grpc.StatusCode.INTERNAL:
            if logger is not None:
                logger.error("internal error", error=str(err))
            return code, "internal error"
        return code, err.message

    # Unknown exception — always internal
    if logger is not None:
        logger.error("unhandled error", error=str(err), type=type(err).__name__)
    return grpc.StatusCode.INTERNAL, "internal error"


def abort_with_error(
    context: grpc.ServicerContext,
    err: Exception,
    logger: Logger | None = None,
) -> None:
    """Set gRPC error status on the servicer context and abort.

    Convenience wrapper combining ``to_grpc_status`` with ``context.abort``.
    """
    code, message = to_grpc_status(err, logger)
    context.abort(code, message)
