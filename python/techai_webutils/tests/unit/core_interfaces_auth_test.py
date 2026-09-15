"""Tests for AuthIdentity dataclass and AuthProvider ABC."""

from techai_webutils.core.domain_types.types import ID
from techai_webutils.core.interfaces.auth import AuthIdentity, AuthProvider
import pytest


class TestAuthIdentity:
    def test_construction(self) -> None:
        """Test that AuthIdentity preserves every field passed to the constructor.

        **Why this test is important:**
          - AuthIdentity is the authenticated principal that every authorization
            decision downstream reads from; a dropped or swapped field (wrong org_id,
            missing clearance_level) silently grants or denies access to the wrong tenant
          - It is the multi-tenant security boundary, so its fields must round-trip exactly

        **What it tests:**
          - user_id, org_id, email, roles, and clearance_level are all stored and
            returned unchanged from the values supplied at construction
        """
        identity = AuthIdentity(
            user_id=ID("u1"),
            org_id=ID("o1"),
            email="user@example.com",
            roles=["admin"],
            clearance_level="confidential",
        )
        assert identity.user_id == ID("u1")
        assert identity.org_id == ID("o1")
        assert identity.email == "user@example.com"
        assert identity.roles == ["admin"]
        assert identity.clearance_level == "confidential"

    def test_defaults(self) -> None:
        """Test that an identity built without roles/clearance defaults to least privilege.

        **Why this test is important:**
          - The safe default for an authenticated principal is no roles and no clearance;
            if these defaulted to a populated or shared value, a caller that forgot to set
            them would accidentally grant elevated access
          - Confirms the defaults are independent per instance (mutable-default-arg trap)

        **What it tests:**
          - Omitting roles yields an empty list and omitting clearance_level yields ""
        """
        identity = AuthIdentity(user_id=ID("u"), org_id=ID("o"), email="a@b.com")
        assert identity.roles == []
        assert identity.clearance_level == ""


class TestAuthProvider:
    def test_cannot_instantiate_abc(self) -> None:
        """Test that AuthProvider cannot be instantiated without a concrete implementation.

        **Why this test is important:**
          - AuthProvider is the abstract contract for token validation and authorization;
            allowing it to be instantiated directly would mean its abstract methods could
            be called and return None, bypassing real auth checks
          - Guarantees every deployed provider supplies real validate_token/has_role logic

        **What it tests:**
          - Instantiating the ABC directly raises TypeError because abstract methods
            (validate_token, has_role, has_permission, tenant_context) are unimplemented
        """
        with pytest.raises(TypeError):
            AuthProvider()  # type: ignore[abstract]
