"""gRPC server interceptor for extracting authentication claims from metadata.

Mirrors Go's ``go/clients/transport/connect/interceptors/auth.go``.

Kong (or another gateway) sets headers after identity-service JWT validation.
This interceptor extracts those headers from gRPC metadata and makes them
available downstream via ``contextvars``.
"""

from __future__ import annotations

import contextvars
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

import grpc

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


@dataclass(frozen=True)
class AuthClaims:
    """Authentication claims extracted from request metadata.

    Matches Go's ``AuthClaims`` struct.
    """

    user_id: str = ""
    """The authenticated user's subject id (from the ``X-User-Sub`` claim)."""
    org_id: str = ""
    """The caller's organization/tenant id (from the ``X-Org-ID`` claim)."""
    clearance_level: str = ""
    """The caller's clearance level, gating which document classifications they may see."""
    roles: frozenset[str] = field(default_factory=frozenset)
    """The caller's authorization roles (from the ``X-Roles`` claim)."""


_auth_claims_var: contextvars.ContextVar[AuthClaims | None] = contextvars.ContextVar(
    "auth_claims",
    default=None,
)


def set_auth_claims(claims: AuthClaims) -> contextvars.Token[AuthClaims | None]:
    """Store auth claims in the current context."""
    return _auth_claims_var.set(claims)


def get_auth_claims() -> AuthClaims | None:
    """Retrieve auth claims from the current context."""
    return _auth_claims_var.get()


def _split_roles(raw: str) -> frozenset[str]:
    """Parse comma-separated roles, discarding empty segments."""
    if not raw:
        return frozenset()
    return frozenset(r.strip() for r in raw.split(",") if r.strip())


class AuthServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Async server interceptor that extracts auth claims from gRPC metadata.

    Only processes authenticated requests (when ``x-user-id`` is present).
    Does NOT validate tokens — that's delegated to Kong + identity service (or, for internal
    service-to-service calls, the service-auth interceptor).
    """

    async def intercept_service(  # type: ignore[override]
        self,
        continuation: Callable[[grpc.HandlerCallDetails], Awaitable[grpc.RpcMethodHandler | None]],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler | None:
        """Extract auth headers and store as AuthClaims in contextvars."""
        # The gRPC Python API for server interceptors passes metadata via
        # handler_call_details.invocation_metadata
        metadata = dict(handler_call_details.invocation_metadata or [])
        user_id = str(metadata.get("x-user-id", ""))

        if user_id:
            claims = AuthClaims(
                user_id=user_id,
                org_id=str(metadata.get("x-org-id", "")),
                clearance_level=str(metadata.get("x-clearance-level", "")),
                roles=_split_roles(str(metadata.get("x-roles", ""))),
            )
            set_auth_claims(claims)

        return await continuation(handler_call_details)
