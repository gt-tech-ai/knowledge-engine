"""Authentication and authorization interfaces.

Mirrors Go's ``interfaces.AuthProvider`` and ``AuthIdentity``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.domain_types.types import ID, TenantContext


@dataclass
class AuthIdentity:
    """Authenticated user/service identity."""

    user_id: ID
    """Stable subject identifier for the authenticated principal (the JWT ``sub`` claim)."""
    org_id: ID
    """Tenant/organization the principal is acting within (the multi-tenant scope key)."""
    email: str
    """The principal's email address, as asserted by the identity provider."""
    roles: list[str] = field(default_factory=list)
    """Role names granted to the principal, used by ``has_role`` for authorization checks."""
    clearance_level: str = ""
    """Security clearance tier gating access to classified documents (empty when unclassified)."""


class AuthProvider(ABC):
    """Handles authentication and authorization."""

    @abstractmethod
    async def validate_token(self, token: str) -> AuthIdentity:
        """Validate a JWT/bearer token and return the authenticated identity."""
        ...

    @abstractmethod
    async def has_role(self, role: str) -> bool:
        """Check if the authenticated user has the specified role."""
        ...

    @abstractmethod
    async def has_permission(self, permission: str, resource: str) -> bool:
        """Check if the authenticated user has a specific permission on a resource."""
        ...

    @abstractmethod
    async def tenant_context(self) -> TenantContext:
        """Extract multi-tenant context from the request."""
        ...
