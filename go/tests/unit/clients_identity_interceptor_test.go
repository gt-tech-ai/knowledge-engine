package unit_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/transport/connect/interceptors"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// invokeStreaming runs the identity interceptor's STREAMING path against a
// pass-through handler and returns the context the handler observed plus the error.
func invokeStreaming(
	t *testing.T, ic connect.Interceptor, ctx context.Context,
) (context.Context, error) {
	t.Helper()
	var captured context.Context
	next := func(c context.Context, _ connect.StreamingHandlerConn) error {
		captured = c
		return nil
	}
	// The identity interceptor's streaming path only enriches the context and hands
	// the conn straight to next (it never reads the conn), so a bare generated mock
	// with no expectations is all this needs — mirrors the connect-interceptor tests.
	conn := mocks.NewMockStreamingHandlerConn(gomock.NewController(t))
	err := ic.WrapStreamingHandler(next)(ctx, conn)
	return captured, err
}

// invokeUnary runs the identity interceptor's unary path against a pass-through
// handler and returns the context the handler observed plus the error.
func invokeUnary(
	ic connect.Interceptor, ctx context.Context,
) (context.Context, error) {
	var captured context.Context
	next := func(c context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		captured = c
		return newTestResponse(), nil
	}
	_, err := ic.WrapUnary(next)(ctx, newTestRequest())
	return captured, err
}

// resolverReturning builds a mock IdentityResolver whose Resolve returns (rc, err).
func resolverReturning(
	t *testing.T, rc *interceptors.UserPermissions, err error,
) interceptors.IdentityResolver {
	res := mocks.NewMockIdentityResolver(gomock.NewController(t))
	res.EXPECT().Resolve(gomock.Any(), gomock.Any(), gomock.Any()).Return(rc, err)
	return res
}

// TestIdentityInterceptor_EnrichesOnSuccess tests that a resolved context carries
// UserPermissions for the handler to read.
//
// Why this test is important:
//   - Handlers read org/teams/workspaces off UserPermissions; if enrichment doesn't
//     land, every workspace-scoped authorization decision is wrong.
//
// What it tests:
//   - With claims present, the interceptor resolves and stores UserPermissions
//     (OrgID, Teams, Workspaces) in the context the handler observes.
func TestIdentityInterceptor_EnrichesOnSuccess(t *testing.T) {
	org := uuid.New()
	team := uuid.New()
	ws := uuid.New()
	res := resolverReturning(t, &interceptors.UserPermissions{
		OrgID:      org,
		Teams:      []interceptors.TeamMembership{{ID: team, Role: "lead"}},
		Workspaces: []interceptors.WorkspaceAccess{{ID: ws, AccessLevel: "admin"}},
	}, nil)
	ic := interceptors.NewIdentityInterceptor(res, false)
	ctx := interceptors.WithAuthClaims(context.Background(),
		&interceptors.AuthClaims{Sub: "sub", OrgID: "org"})

	captured, err := invokeUnary(ic, ctx)

	require.NoError(t, err)
	perms, ok := interceptors.GetUserPermissions(captured)
	require.True(t, ok)
	require.Equal(t, org, perms.OrgID)
	require.Equal(
		t,
		[]interceptors.TeamMembership{{ID: team, Role: "lead"}},
		perms.Teams,
	)
	require.Equal(
		t,
		[]interceptors.WorkspaceAccess{{ID: ws, AccessLevel: "admin"}},
		perms.Workspaces,
	)
}

// TestIdentityInterceptor_ClearsRawClaimsAfterResolve tests that a successful
// resolve clears the raw AuthClaims from the context handed to the next handler.
//
// Why this test is important:
//   - Once UserPermissions is resolved, AuthClaims is stale/unresolved data; if a
//     handler read it instead of UserPermissions it would silently authorize on
//     unenriched claims (e.g. no Teams/Workspaces). Clearing it forces every
//     downstream consumer onto the single, properly-typed source of truth.
//
// What it tests:
//   - After a successful resolve, GetAuthClaims on the handler's context no longer
//     returns the caller's original AuthClaims pointer.
func TestIdentityInterceptor_ClearsRawClaimsAfterResolve(t *testing.T) {
	res := resolverReturning(t, &interceptors.UserPermissions{OrgID: uuid.New()}, nil)
	ic := interceptors.NewIdentityInterceptor(res, false)
	original := &interceptors.AuthClaims{Sub: "sub", OrgID: "org"}
	ctx := interceptors.WithAuthClaims(context.Background(), original)

	captured, err := invokeUnary(ic, ctx)

	require.NoError(t, err)
	claims, ok := interceptors.GetAuthClaims(captured)
	require.True(t, ok, "AuthClaims key stays populated (to nil), not removed")
	assert.Nil(t, claims, "raw claims must be cleared once UserPermissions is resolved")
}

// TestIdentityInterceptor_NoClaims_NoOp tests that a request with no auth claims
// passes through without resolving.
//
// Why this test is important:
//   - Public/unauthenticated routes must still work; resolving (and fail-closing)
//     when there's nothing to resolve would break them.
//
// What it tests:
//   - With no AuthClaims in context, the interceptor does not call the resolver
//     (the mock has no expectation) and returns no error.
func TestIdentityInterceptor_NoClaims_NoOp(t *testing.T) {
	res := mocks.NewMockIdentityResolver(
		gomock.NewController(t),
	) // no EXPECT: must not resolve
	ic := interceptors.NewIdentityInterceptor(res, false)

	_, err := invokeUnary(ic, context.Background())

	require.NoError(t, err)
}

// TestIdentityInterceptor_TransientResolverError_FailsClosedRetryable tests that
// an uncoded (transient/infra) resolver error fails the request closed with a
// RETRYABLE code (Unavailable), not Unauthenticated.
//
// Why this test is important:
//   - If identity can't resolve the caller's context, proceeding would grant or deny
//     on incomplete information; the request must be rejected. But a transient identity
//     outage must surface as retryable — NOT a 401 that logs every uncached user out.
//
// What it tests:
//   - An uncoded resolver error with claims present yields a CodeUnavailable Connect
//     error (fail-closed, but retryable).
func TestIdentityInterceptor_TransientResolverError_FailsClosedRetryable(
	t *testing.T,
) {
	ic := interceptors.NewIdentityInterceptor(
		resolverReturning(t, nil, errors.New("identity down")), false,
	)
	ctx := interceptors.WithAuthClaims(
		context.Background(),
		&interceptors.AuthClaims{Sub: "sub"},
	)

	_, err := invokeUnary(ic, ctx)

	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// TestIdentityInterceptor_AuthResolverError_PreservesCode tests that a coded auth
// failure from the resolver (e.g. unknown user) is preserved as Unauthenticated.
//
// Why this test is important:
//   - A genuine "user not found" must reject the caller (re-auth), not be masked as a
//     retryable outage; the interceptor must distinguish it from a transient failure.
//
// What it tests:
//   - A resolver error already coded Unauthenticated is passed through unchanged.
func TestIdentityInterceptor_AuthResolverError_PreservesCode(t *testing.T) {
	ic := interceptors.NewIdentityInterceptor(
		resolverReturning(t, nil,
			connect.NewError(connect.CodeUnauthenticated, errors.New("unknown user"))),
		false,
	)
	ctx := interceptors.WithAuthClaims(
		context.Background(),
		&interceptors.AuthClaims{Sub: "sub"},
	)

	_, err := invokeUnary(ic, ctx)

	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// TestIdentityInterceptor_StubMode_SyntheticClaims_PopulatesStub tests that stub
// mode enriches synthetic (no-gateway) claims deterministically without calling the
// resolver.
//
// Why this test is important:
//   - Local dev (auth.stub) must work without the identity service reachable;
//     calling (and fail-closing on) the resolver would break it.
//
// What it tests:
//   - In stub mode, SYNTHETIC claims produce UserPermissions with OrgID set to the
//     stub org and the resolver is never called (the mock has no expectation).
func TestIdentityInterceptor_StubMode_SyntheticClaims_PopulatesStub(t *testing.T) {
	res := mocks.NewMockIdentityResolver(
		gomock.NewController(t),
	) // no EXPECT: synthetic stub never resolves
	ic := interceptors.NewIdentityInterceptor(res, true)
	ctx := interceptors.WithAuthClaims(
		context.Background(),
		&interceptors.AuthClaims{Sub: "stub-user", Synthetic: true},
	)

	captured, err := invokeUnary(ic, ctx)

	require.NoError(t, err)
	perms, ok := interceptors.GetUserPermissions(captured)
	require.True(t, ok)
	require.Equal(t, interceptors.StubInternalOrgID, perms.OrgID)
	require.Equal(t, "member", perms.OrgRole)
}

// TestIdentityInterceptor_StubMode_RealClaims_Resolves tests that stub mode does
// NOT force the stub org onto real (Kong-injected) claims — it resolves them.
//
// Why this test is important:
//   - The stack runs stub=true behind Kong so it also works without Kong. A real
//     caller's Kong-injected claims must resolve to their TRUE org; forcing the stub
//     org would collapse every tenant into one and break multi-tenant isolation.
//
// What it tests:
//   - In stub mode, non-synthetic claims bypass the stub short-circuit and are
//     resolved (OrgID comes from the resolver, not the stub org).
func TestIdentityInterceptor_StubMode_RealClaims_Resolves(t *testing.T) {
	realOrg := uuid.New()
	res := resolverReturning(t, &interceptors.UserPermissions{
		OrgID: realOrg,
	}, nil)
	ic := interceptors.NewIdentityInterceptor(res, true)
	ctx := interceptors.WithAuthClaims(context.Background(),
		&interceptors.AuthClaims{Sub: "real-sub", OrgID: "real-ext-org"})

	captured, err := invokeUnary(ic, ctx)

	require.NoError(t, err)
	perms, ok := interceptors.GetUserPermissions(captured)
	require.True(t, ok)
	require.Equal(t, realOrg, perms.OrgID)
	require.NotEqual(t, interceptors.StubInternalOrgID, perms.OrgID)
}

// TestIdentityInterceptor_ResolvesOrgRole tests that the resolved org role lands
// on UserPermissions.
//
// Why this test is important:
//   - Org-admin authorization checks UserPermissions.IsOrgAdmin/OrgRole, but Auth0's
//     JWT does not carry the org role reliably (X-Roles is empty in staging). The
//     role must be resolved server-side, or an org admin is denied (403) on
//     grant/revoke and other admin-only operations.
//
// What it tests:
//   - A resolver returning OrgRole "admin" results in "admin" on the resolved
//     UserPermissions and IsOrgAdmin() returning true.
func TestIdentityInterceptor_ResolvesOrgRole(t *testing.T) {
	res := resolverReturning(t, &interceptors.UserPermissions{
		OrgID:   uuid.New(),
		OrgRole: "admin",
	}, nil)
	ic := interceptors.NewIdentityInterceptor(res, false)
	ctx := interceptors.WithAuthClaims(context.Background(),
		&interceptors.AuthClaims{Sub: "sub", OrgID: "org"})

	captured, err := invokeUnary(ic, ctx)

	require.NoError(t, err)
	perms, ok := interceptors.GetUserPermissions(captured)
	require.True(t, ok)
	assert.Equal(t, "admin", perms.OrgRole)
	assert.True(t, perms.IsOrgAdmin(),
		"resolved org-admin role must make IsOrgAdmin true for authorization")
}

// TestIdentityInterceptor_EnrichesOnStreaming tests that the STREAMING path
// resolves and stores UserPermissions in the context.
//
// Why this test is important:
//   - Connect applies a unary interceptor only to unary calls; without streaming
//     coverage, server-streaming RPCs (e.g. QueryStream) would run with an empty
//     authorization context — the exact gap this interceptor now closes
//
// What it tests:
//   - WrapStreamingHandler resolves and stores UserPermissions (OrgID/Teams/
//     Workspaces) the streaming handler observes.
func TestIdentityInterceptor_EnrichesOnStreaming(t *testing.T) {
	org := uuid.New()
	team := uuid.New()
	ws := uuid.New()
	res := resolverReturning(t, &interceptors.UserPermissions{
		OrgID:      org,
		Teams:      []interceptors.TeamMembership{{ID: team, Role: "lead"}},
		Workspaces: []interceptors.WorkspaceAccess{{ID: ws, AccessLevel: "admin"}},
	}, nil)
	ic := interceptors.NewIdentityInterceptor(res, false)
	ctx := interceptors.WithAuthClaims(context.Background(),
		&interceptors.AuthClaims{Sub: "sub", OrgID: "org"})

	captured, err := invokeStreaming(t, ic, ctx)

	require.NoError(t, err)
	perms, ok := interceptors.GetUserPermissions(captured)
	require.True(t, ok)
	require.Equal(t, org, perms.OrgID)
}

// TestIdentityInterceptor_StreamingResolverError_FailsClosedRetryable tests that a
// transient resolver error fails a STREAMING request closed with a retryable code.
//
// Why this test is important:
//   - A streaming call must not proceed without an authorization context any more
//     than a unary one; the fail-closed (retryable on transient failure) guarantee
//     has to hold on both paths
//
// What it tests:
//   - An uncoded resolver error on the streaming path yields a CodeUnavailable error
//     and does not invoke the handler
func TestIdentityInterceptor_StreamingResolverError_FailsClosedRetryable(
	t *testing.T,
) {
	ic := interceptors.NewIdentityInterceptor(
		resolverReturning(t, nil, errors.New("identity down")), false,
	)
	ctx := interceptors.WithAuthClaims(
		context.Background(),
		&interceptors.AuthClaims{Sub: "sub"},
	)

	_, err := invokeStreaming(t, ic, ctx)

	require.Error(t, err)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// ---------------------------------------------------------------------------
// UserPermissions / TeamMembership — pure method behavior
// ---------------------------------------------------------------------------

// TestUserPermissions_IsOrgAdmin tests the org-admin predicate.
//
// Why this test is important:
//   - This single method gates every org-admin bypass (e.g. the workspace
//     owner-only guard); a wrong result over- or under-grants admin privilege.
//
// What it tests:
//   - OrgRole "admin" -> true; any other role (including empty) -> false.
func TestUserPermissions_IsOrgAdmin(t *testing.T) {
	t.Parallel()

	assert.True(t, (&interceptors.UserPermissions{OrgRole: "admin"}).IsOrgAdmin())
	assert.False(t, (&interceptors.UserPermissions{OrgRole: "member"}).IsOrgAdmin())
	assert.False(t, (&interceptors.UserPermissions{}).IsOrgAdmin())
}

// TestUserPermissions_HasWorkspaceAccess tests the workspace-membership predicate.
//
// Why this test is important:
//   - Handlers use this to decide whether the caller may reach a workspace at all;
//     a false positive/negative directly changes what documents a caller can see.
//
// What it tests:
//   - A workspace id present in Workspaces -> true; an absent id -> false.
func TestUserPermissions_HasWorkspaceAccess(t *testing.T) {
	t.Parallel()

	granted := uuid.New()
	other := uuid.New()
	perms := &interceptors.UserPermissions{
		Workspaces: []interceptors.WorkspaceAccess{{ID: granted, AccessLevel: "read"}},
	}

	assert.True(t, perms.HasWorkspaceAccess(granted))
	assert.False(t, perms.HasWorkspaceAccess(other))
}

// TestUserPermissions_EffectiveAccess tests that EffectiveAccess reports the
// caller's highest access level for a workspace across their grants.
//
// Why this test is important:
//   - Write-gated document ops (delete/upload) decide from the *level*, not mere
//     presence; if two team grants on the same workspace disagree, the caller must
//     get the strongest one, and an ungranted workspace must report no access — a
//     wrong level here lets a read-only member mutate, or blocks a writer.
//
// What it tests:
//   - admin > write > read precedence; the max across duplicate workspace grants
//     wins; an absent workspace yields "".
func TestUserPermissions_EffectiveAccess(t *testing.T) {
	t.Parallel()

	ws := uuid.New()
	other := uuid.New()
	perms := &interceptors.UserPermissions{
		Workspaces: []interceptors.WorkspaceAccess{
			{ID: ws, AccessLevel: "read"},
			{ID: ws, AccessLevel: "admin"},
			{ID: ws, AccessLevel: "write"},
		},
	}

	assert.Equal(
		t,
		"admin",
		perms.EffectiveAccess(ws),
		"highest of the workspace's grants wins",
	)
	assert.Equal(t, "", perms.EffectiveAccess(other), "no grant -> no access")
	assert.Equal(
		t, "read",
		(&interceptors.UserPermissions{
			Workspaces: []interceptors.WorkspaceAccess{{ID: ws, AccessLevel: "read"}},
		}).EffectiveAccess(ws),
	)
}

// TestUserPermissions_CanWriteWorkspace tests the write-authorization predicate.
//
// Why this test is important:
//   - This is the single gate for document mutations (delete/upload/confirm/
//     complete/abort); a read-only member must be denied, a write/admin member and
//
// any org admin allowed — the exact boundary audit F5 / enforces.
//
// What it tests:
//   - read -> false; write/admin -> true; an org admin with no workspace grant ->
//     true (the org-admin superset); no grant and not admin -> false.
func TestUserPermissions_CanWriteWorkspace(t *testing.T) {
	t.Parallel()

	ws := uuid.New()
	perm := func(role, level string) *interceptors.UserPermissions {
		p := &interceptors.UserPermissions{OrgRole: role}
		if level != "" {
			p.Workspaces = []interceptors.WorkspaceAccess{{ID: ws, AccessLevel: level}}
		}
		return p
	}

	assert.False(
		t,
		perm("member", "read").CanWriteWorkspace(ws),
		"read-only member cannot write",
	)
	assert.True(
		t,
		perm("member", "write").CanWriteWorkspace(ws),
		"write member can write",
	)
	assert.True(
		t,
		perm("member", "admin").CanWriteWorkspace(ws),
		"workspace admin can write",
	)
	assert.True(
		t,
		perm("admin", "").CanWriteWorkspace(ws),
		"org admin can write without a grant",
	)
	assert.False(
		t,
		perm("member", "").CanWriteWorkspace(ws),
		"no grant and not org admin -> denied",
	)
}

// TestUserPermissions_TeamIDs tests that TeamIDs projects the caller's team
// memberships into a bare id slice, preserving order.
//
// Why this test is important:
//   - Workspace team-access checks (EffectiveAccess, ListForTeams) consume this
//     projection directly; a dropped or reordered id would mis-scope those checks.
//
// What it tests:
//   - Multiple teams project in order; no teams yields an empty (non-nil) slice.
func TestUserPermissions_TeamIDs(t *testing.T) {
	t.Parallel()

	a, b := uuid.New(), uuid.New()
	perms := &interceptors.UserPermissions{
		Teams: []interceptors.TeamMembership{{ID: a}, {ID: b}},
	}

	assert.Equal(t, []uuid.UUID{a, b}, perms.TeamIDs())
	assert.Empty(t, (&interceptors.UserPermissions{}).TeamIDs())
}

// TestTeamMembership_IsTeamLead tests the team-lead predicate.
//
// Why this test is important:
//   - Team-lead status gates team-management operations; a wrong result mis-grants
//     or mis-denies that privilege.
//
// What it tests:
//   - Role "lead" -> true; any other role -> false.
func TestTeamMembership_IsTeamLead(t *testing.T) {
	t.Parallel()

	assert.True(t, (&interceptors.TeamMembership{Role: "lead"}).IsTeamLead())
	assert.False(t, (&interceptors.TeamMembership{Role: "member"}).IsTeamLead())
}
