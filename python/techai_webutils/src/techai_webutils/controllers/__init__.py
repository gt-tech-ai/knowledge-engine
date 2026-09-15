"""Controllers layer - thin transport adapters.

Controllers are the transport layer between external requests (HTTP, gRPC, WebSocket)
and internal business logic (workflows, services). They deserialize requests, delegate
to workflows, and serialize responses.

Controllers implements the eighth layer in the nine-layer architecture model:
core -> foundation -> clients -> repos -> services -> pipelines -> workflows -> controllers
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable

from techai_webutils.controllers.base import BaseController, ErrorResponse, map_error_to_http_status
from techai_webutils.controllers.decorators import HandlerBuilder

# Named generic handler type, matching Go's ``HandlerFunc[Req, Resp]``
# in ``pkg/go/transport/handler.go``.
type HandlerFunc = Callable[..., Awaitable[object]]

__all__ = [
    "BaseController",
    "ErrorResponse",
    "HandlerBuilder",
    "HandlerFunc",
    "map_error_to_http_status",
]
