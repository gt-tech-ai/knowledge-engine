"""Tests for gRPC auth interceptor and AuthClaims extraction."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.rpc.grpc.interceptors.auth import (
    HeaderClaimMapping,
    AuthClaims,
    AuthServerInterceptor,
    _auth_claims_var,
    _split_roles,
    get_auth_claims,
    set_auth_claims,
)


_HEADERS = HeaderClaimMapping(user_id="x-user-id", tenant_id="x-org-id", roles="x-roles")
"""The gateway header contract these tests send."""


class TestAuthClaims:
    """Test suite for AuthClaims dataclass."""

    def test_default_values(self) -> None:
        """Test that a default-constructed AuthClaims represents an anonymous principal.

        **Why this test is important:**
          - Downstream authorization treats empty identity fields as "no identity"
          - A non-empty default (e.g. a stray role) would silently grant access nobody intended
          - Confirms the safe, deny-by-default starting point for every claims object

        **What it tests:**
          - user_id and tenant_id default to the empty string
          - roles defaults to an empty frozenset (no privileges)
        """
        claims = AuthClaims()
        assert claims.user_id == ""
        assert claims.tenant_id == ""
        assert claims.roles == frozenset()

    def test_constructed_values(self) -> None:
        """Test that explicitly supplied identity fields are retained verbatim.

        **Why this test is important:**
          - Claims carry the authenticated user, tenant, and roles to business logic
          - Any corruption or drop of these fields would mis-attribute or mis-authorize a request
          - Confirms the dataclass faithfully stores the gateway-provided identity

        **What it tests:**
          - user_id and tenant_id round-trip the constructor arguments
          - A role passed in is present in the resulting roles set
        """
        claims = AuthClaims(
            user_id="u-1",
            tenant_id="o-1",
            roles=frozenset({"admin", "member"}),
        )
        assert claims.user_id == "u-1"
        assert claims.tenant_id == "o-1"
        assert "admin" in claims.roles

    def test_frozen(self) -> None:
        """Test that AuthClaims is immutable after construction.

        **Why this test is important:**
          - Claims are shared across a request via contextvars; in-place mutation would be a security hazard
          - Immutability guarantees that no handler can escalate its own privileges by editing the claims
          - Confirms the frozen dataclass contract that the auth model relies on

        **What it tests:**
          - Assigning to user_id on an existing instance raises AttributeError
        """
        import pytest

        claims = AuthClaims(user_id="u-1")
        with pytest.raises(AttributeError):
            claims.user_id = "u-2"  # type: ignore[misc]


class TestSplitRoles:
    """Test suite for role string parsing."""

    def test_empty_string(self) -> None:
        """Test that an empty x-roles header parses to no roles.

        **Why this test is important:**
          - A request with no roles header must grant zero privileges, not crash or guess
          - Treating an empty string as a role would inject a bogus "" role into authorization checks
          - Confirms the deny-by-default behavior for role-free requests

        **What it tests:**
          - _split_roles("") returns an empty frozenset
        """
        assert _split_roles("") == frozenset()

    def test_single_role(self) -> None:
        """Test that a single-role header parses to exactly that role.

        **Why this test is important:**
          - Single-role principals are the common case and must map to the right privilege
          - A parsing slip here would deny or over-grant access for the most frequent request shape
          - Confirms the base case of the comma-split logic

        **What it tests:**
          - _split_roles("admin") returns a frozenset containing only "admin"
        """
        assert _split_roles("admin") == frozenset({"admin"})

    def test_multiple_roles(self) -> None:
        """Test that a comma-separated header yields every distinct role.

        **Why this test is important:**
          - Multi-role users (e.g. admin + member) need all their roles surfaced for authorization
          - Dropping a role would deny legitimate access; inventing one would over-grant
          - Confirms the splitter preserves each role across separators

        **What it tests:**
          - _split_roles("admin,member,viewer") returns all three roles as a frozenset
        """
        assert _split_roles("admin,member,viewer") == frozenset({"admin", "member", "viewer"})

    def test_strips_whitespace(self) -> None:
        """Test that surrounding whitespace around roles is trimmed.

        **Why this test is important:**
          - Gateways may emit padded role lists; " admin" must match the "admin" privilege check
          - Untrimmed roles would silently never match any authorization rule, locking users out
          - Confirms normalization so equality comparisons against role names work

        **What it tests:**
          - _split_roles(" admin , member ") returns the trimmed roles {"admin", "member"}
        """
        assert _split_roles(" admin , member ") == frozenset({"admin", "member"})

    def test_discards_empty_segments(self) -> None:
        """Test that empty segments from trailing or doubled commas are dropped.

        **Why this test is important:**
          - Malformed headers ("admin,,member,") must not introduce an empty "" role
          - An empty role in the set would be a spurious privilege token in authorization logic
          - Confirms the parser is resilient to sloppy header formatting

        **What it tests:**
          - _split_roles("admin,,member,") returns only the real roles {"admin", "member"}
        """
        assert _split_roles("admin,,member,") == frozenset({"admin", "member"})


class TestContextVars:
    """Test suite for auth claims context management."""

    def test_set_and_get_claims(self) -> None:
        """Test that claims stored in the current context are retrievable by handlers.

        **Why this test is important:**
          - The interceptor sets claims and downstream handlers read them via this same contextvar
          - A broken set/get round-trip would leave every authenticated handler without an identity
          - Confirms the contextvar plumbing that the entire per-request auth model depends on

        **What it tests:**
          - get_auth_claims() after set_auth_claims() returns the same user_id and tenant_id
        """
        claims = AuthClaims(user_id="u-1", tenant_id="o-1")
        token = set_auth_claims(claims)
        try:
            retrieved = get_auth_claims()
            assert retrieved is not None
            assert retrieved.user_id == "u-1"
            assert retrieved.tenant_id == "o-1"
        finally:
            from techai_webutils.clients.rpc.grpc.interceptors.auth import _auth_claims_var

            _auth_claims_var.reset(token)

    def test_default_is_none(self) -> None:
        """Test that a context with no claims set reports None, not a default identity.

        **Why this test is important:**
          - Absent claims must read as None so handlers can reject or treat the request as anonymous
          - A non-None default would fabricate an identity for unauthenticated requests (privilege bug)
          - Confirms the deny-by-default behavior at the context layer

        **What it tests:**
          - get_auth_claims() returns None when the contextvar holds no claims
        """
        # In a fresh context, claims should be None
        from techai_webutils.clients.rpc.grpc.interceptors.auth import _auth_claims_var

        token = _auth_claims_var.set(None)
        try:
            assert get_auth_claims() is None
        finally:
            _auth_claims_var.reset(token)


class TestAuthServerInterceptor:
    """Test suite for AuthServerInterceptor metadata extraction into context."""

    @staticmethod
    def _details(metadata: list[tuple[str, str]]) -> MagicMock:
        """Build minimal HandlerCallDetails-shaped data carrying invocation metadata."""
        return MagicMock(invocation_metadata=metadata)

    @pytest.mark.asyncio
    async def test_populates_claims_when_user_present(self) -> None:
        """Test that auth headers in metadata are extracted into context claims.

        **Why this test is important:**
          - Downstream handlers read identity exclusively from these context claims
          - A gateway-authenticated request must surface its user/org/roles to business logic
          - Getting the header-to-claim mapping wrong would mis-attribute or leak access

        **What it tests:**
          - After interception, the context claims carry the user, tenant, and roles
          - The continuation is invoked with the original handler call details
        """
        details = self._details(
            [
                ("x-user-id", "u-1"),
                ("x-org-id", "o-1"),
                ("x-roles", "admin,member"),
            ]
        )
        continuation = AsyncMock(return_value="handler")
        token = _auth_claims_var.set(None)
        try:
            result = await AuthServerInterceptor(_HEADERS).intercept_service(continuation, details)

            claims = get_auth_claims()
            assert result == "handler"
            assert claims is not None
            assert claims.user_id == "u-1"
            assert claims.tenant_id == "o-1"
            assert claims.roles == frozenset({"admin", "member"})
            continuation.assert_called_once_with(details)
        finally:
            _auth_claims_var.reset(token)

    @pytest.mark.asyncio
    async def test_skips_claims_when_user_absent(self) -> None:
        """Test that an unauthenticated request sets no claims but still proceeds.

        **Why this test is important:**
          - Health checks and pre-auth calls legitimately arrive without identity headers
          - The interceptor must not fabricate claims for them (would be a privilege bug)
          - It must still forward the call so unauthenticated endpoints keep working

        **What it tests:**
          - No claims are stored in context when x-user-id is missing
          - The continuation is still invoked with the handler call details
        """
        details = self._details([("x-org-id", "o-1")])
        continuation = AsyncMock(return_value="handler")
        token = _auth_claims_var.set(None)
        try:
            result = await AuthServerInterceptor(_HEADERS).intercept_service(continuation, details)

            assert result == "handler"
            assert get_auth_claims() is None
            continuation.assert_called_once_with(details)
        finally:
            _auth_claims_var.reset(token)

    @pytest.mark.asyncio
    async def test_reads_claims_from_the_consumers_header_mapping(self) -> None:
        """A consumer's header mapping decides which metadata keys become claims.

        **Why this test is important:**
          - Gateways name identity headers differently; a fixed mapping ties the library to one
            deployment's header contract.

        **What it tests:**
          - With a custom HeaderClaimMapping, claims come from the custom keys.
          - A key outside the mapping (``x-user-id``) is ignored.
        """
        mapping = HeaderClaimMapping(user_id="x-sub", tenant_id="x-tenant", roles="x-groups")
        interceptor = AuthServerInterceptor(headers=mapping)
        continuation = AsyncMock(return_value="handler")
        token = _auth_claims_var.set(None)
        try:
            await interceptor.intercept_service(
                continuation,
                self._details([("x-sub", "u-1"), ("x-tenant", "t-1"), ("x-groups", "a,b")]),
            )
            assert get_auth_claims() == AuthClaims(
                user_id="u-1", tenant_id="t-1", roles=frozenset({"a", "b"})
            )

            _auth_claims_var.set(None)
            await interceptor.intercept_service(continuation, self._details([("x-user-id", "u-1")]))
            assert get_auth_claims() is None
        finally:
            _auth_claims_var.reset(token)
