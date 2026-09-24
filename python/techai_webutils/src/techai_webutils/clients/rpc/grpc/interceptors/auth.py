"""gRPC server interceptor for extracting authentication claims from metadata.

Mirrors Go's ``go/clients/transport/connect/interceptors/auth.go``.

A gateway sets identity headers after validating the caller's token. This
interceptor extracts those headers from gRPC metadata — under the names in its
``HeaderClaimMapping`` — and makes them available downstream via ``contextvars``.
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
    """The authenticated user's subject id (default metadata key ``x-user-id``)."""
    org_id: str = ""
    """The caller's organization/tenant id (default metadata key ``x-org-id``)."""
    clearance_level: str = ""
    """The caller's clearance level (default metadata key ``x-clearance-level``)."""
    roles: frozenset[str] = field(default_factory=frozenset)
    """The caller's authorization roles (default metadata key ``x-roles``, comma-separated)."""


@dataclass(frozen=True, slots=True)
class HeaderClaimMapping:
    """The gRPC metadata keys (lowercase) each claim is read from, so a consumer matches its gateway."""

    user_id: str = "x-user-id"
    """Key carrying the subject id; its absence means the request is unauthenticated."""
    org_id: str = "x-org-id"
    """Key carrying the organization/tenant id."""
    clearance_level: str = "x-clearance-level"
    """Key carrying the clearance level."""
    roles: str = "x-roles"
    """Key carrying the comma-separated roles."""


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

    Only processes authenticated requests (when the mapping's ``user_id`` key is present).
    Does NOT validate tokens — that's delegated to the gateway (or, for internal
    service-to-service calls, the service-auth interceptor).
    """

    def __init__(self, headers: HeaderClaimMapping | None = None) -> None:
        """Read claims from the metadata keys in ``headers`` (default ``HeaderClaimMapping()``)."""
        self._headers = headers or HeaderClaimMapping()

    async def intercept_service(  # type: ignore[override]
        self,
        continuation: Callable[[grpc.HandlerCallDetails], Awaitable[grpc.RpcMethodHandler | None]],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler | None:
        """Extract auth headers and store as AuthClaims in contextvars."""
        # The gRPC Python API for server interceptors passes metadata via
        # handler_call_details.invocation_metadata
        metadata = dict(handler_call_details.invocation_metadata or [])
        keys = self._headers
        user_id = str(metadata.get(keys.user_id, ""))

        if user_id:
            claims = AuthClaims(
                user_id=user_id,
                org_id=str(metadata.get(keys.org_id, "")),
                clearance_level=str(metadata.get(keys.clearance_level, "")),
                roles=_split_roles(str(metadata.get(keys.roles, ""))),
            )
            set_auth_claims(claims)

        return await continuation(handler_call_details)
