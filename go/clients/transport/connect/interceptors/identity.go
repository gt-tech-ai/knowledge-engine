package interceptors

import (
	"context"
	"slices"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// StubInternalOrgID is the internal org UUID injected for the local-dev stub user
// (auth.stub=true) when identity is not resolved over gRPC. It matches the seeded
// development organization.
var StubInternalOrgID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// StubOrgExternalID is the synthetic Auth0 org id injected for the local-dev stub user, so the
// org-prefixed storage key has a stable non-empty org segment in dev (uploads fail closed
// on an empty org). Under the dev shared-KB topology the value is not used for routing.
const StubOrgExternalID = "org_stub_dev"

// userPermsContextKey is the private context key under which UserPermissions are stored,
// keeping the permissions slot collision-free across packages.
type userPermsContextKey struct{}

// WithUserPermissions stores resolved user permissions in the context.
func WithUserPermissions(
	ctx context.Context,
	userPerms *UserPermissions,
) context.Context {
	return context.WithValue(ctx, userPermsContextKey{}, userPerms)
}

// GetUserPermissions retrieves resolved user permissions from the context.
func GetUserPermissions(ctx context.Context) (*UserPermissions, bool) {
	claims, ok := ctx.Value(userPermsContextKey{}).(*UserPermissions)
	return claims, ok
}

// RequireUserPermissions retrieves the caller's resolved permissions from the context, returning a
// 401 Unauthorized AppError when the request is unauthenticated (no permissions present). It is the
// get-or-401 convenience over [GetUserPermissions] every authenticated handler/service needs — use it
// instead of re-implementing the `if !ok { return Unauthorized(...) }` branch at each call site.
func RequireUserPermissions(ctx context.Context) (*UserPermissions, error) {
	perms, ok := GetUserPermissions(ctx)
	if !ok {
		return nil, errors.Unauthorized("authentication required")
	}
	return perms, nil
}

// TeamMembership is a resolved team membership stored on AuthClaims.
type TeamMembership struct {
	// Role is the caller's team role ("lead" or "member").
	Role string
	// ID is the team's internal ID.
	ID uuid.UUID
}

// IsTeamLead returns true if the caller holds the lead role for the given team.
func (m *TeamMembership) IsTeamLead() bool {
	return m.Role == "lead"
}

// WorkspaceAccess is a resolved workspace grant stored on AuthClaims.
type WorkspaceAccess struct {
	// AccessLevel is the granting team's workspace access ("read", "write", or
	// "admin").
	AccessLevel string
	// ID is the workspace's internal ID.
	ID uuid.UUID
}

// UserPermissions defines the requester's permissions within the organization
// under which the request is made. Permissions are determined by the Identity Service.
type UserPermissions struct {
	// OrgRole is the caller's role in the org ("admin"/"member").
	OrgRole string
	// Clearance is the caller's document-access clearance in the org ("public"/"internal"/"confidential"/"restricted")
	Clearance string
	// OrgExternalID is the caller's Auth0 org id (org_external_id / the X-Org-ID header) — the
	// per-org KB routing key. Retained from the resolve request so the query
	// path can route retrieval per org without a second lookup. Empty for a caller with no org.
	OrgExternalID string
	// ExternalSub is the caller's Auth0 subject (X-User-Sub). Retained from the resolve request
	// (the raw AuthClaims are cleared downstream) so a handler can identify the caller by their
	// external id — e.g. to send it as the granter on a workspace-team grant RPC
	// which identity re-resolves to re-verify authority.
	ExternalSub string
	// Teams is the caller's team memberships within the organization.
	Teams []TeamMembership
	// Workspaces is the caller's accessible workspaces via team grants.
	Workspaces []WorkspaceAccess
	// OrgID is the caller's internal organization UUID.
	OrgID uuid.UUID
	// UserID is the caller's internal user UUID.
	UserID uuid.UUID
}

// IsOrgAdmin returns true if the caller is an admin of the organization.
func (p *UserPermissions) IsOrgAdmin() bool {
	return p.OrgRole == "admin"
}

// HasWorkspaceAccess returns true if the caller has any access to the given workspace.
func (p *UserPermissions) HasWorkspaceAccess(workspaceID uuid.UUID) bool {
	return slices.ContainsFunc(p.Workspaces, func(w WorkspaceAccess) bool {
		return w.ID == workspaceID
	})
}

// EffectiveAccess returns the caller's highest access level ("admin" > "write" >
// "read") for the workspace across all of their team grants, or "" when they have no
// grant on it. A caller on two teams with different access to the same workspace gets
// the stronger of the two.
func (p *UserPermissions) EffectiveAccess(workspaceID uuid.UUID) string {
	best := 0
	level := ""
	for _, w := range p.Workspaces {
		if w.ID != workspaceID {
			continue
		}
		if rank := accessRank(w.AccessLevel); rank > best {
			best, level = rank, w.AccessLevel
		}
	}
	return level
}

// CanWriteWorkspace reports whether the caller may perform a write on the workspace:
// an org admin always may (the admin superset); otherwise their effective access must
// be "write" or "admin". This is the single write-authorization gate for document
// mutations.
func (p *UserPermissions) CanWriteWorkspace(workspaceID uuid.UUID) bool {
	return p.IsOrgAdmin() ||
		accessRank(p.EffectiveAccess(workspaceID)) >= accessRank("write")
}

// accessRank orders the workspace access levels so they can be compared; an unknown or
// empty level ranks 0 (no access).
func accessRank(level string) int {
	switch level {
	case "admin":
		return 3
	case "write":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}

// TeamIDs returns the internal IDs of all teams to which the caller belongs.
func (p *UserPermissions) TeamIDs() []uuid.UUID {
	teamIDs := make([]uuid.UUID, len(p.Teams))
	for i, team := range p.Teams {
		teamIDs[i] = team.ID
	}
	return teamIDs
}

// IdentityResolver resolves a caller's internal authorization context from their
// authenticated (external sub, org external id). It is implemented by the consuming
// service's identity client.
//
// interface-composition exemption — narrow consumer-side seam: a single Resolve(sub, org) → UserPermissions
// authorization port, not an id-CRUD data-access surface, so it embeds no foundation generic.
type IdentityResolver interface {
	// Resolve maps a caller's external (sub, org external id) to their internal
	// authorization context (org, teams, accessible workspaces).
	Resolve(
		ctx context.Context,
		externalSub, orgExternalID string,
	) (*UserPermissions, error)
}

// identityInterceptor enriches the request's AuthClaims with the caller's
// resolved internal org id, teams, and accessible workspaces, for BOTH
// unary and streaming RPCs. It must run AFTER the auth interceptor so the claims
// (Sub, OrgID) are present. Streaming coverage matters because a unary-only
// enrichment would leave server-streaming handlers (e.g. QueryStream) with an empty
// authorization context.
type identityInterceptor struct {
	// resolver resolves (sub, org) → the caller's internal context.
	resolver IdentityResolver
	// stub sets a deterministic stub context without calling the resolver (local dev).
	stub bool
}

// NewIdentityInterceptor returns the identity enrichment interceptor.
//
// Behavior (per RPC, unary or streaming):
//   - no AuthClaims in context (unauthenticated/public request) → pass through
//   - stub mode (auth.stub=true) → set a deterministic stub context, no resolver call
//   - otherwise → resolve and enrich; a resolver error fails the request CLOSED
//     (CodeUnauthenticated) rather than proceeding without an authorization context
func NewIdentityInterceptor(
	resolver IdentityResolver,
	stub bool,
) connect.Interceptor {
	return identityInterceptor{resolver: resolver, stub: stub}
}

// WrapUnary enriches the unary request's claims, failing closed on a resolver error.
func (u identityInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resolvedCtx, err := u.resolve(ctx)
		if err != nil {
			return nil, err
		}
		return next(resolvedCtx, req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side enrichment interceptor.
func (u identityInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler enriches the streaming request's claims, failing closed on a
// resolver error so a streaming call cannot proceed without an authorization context.
func (u identityInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		resolvedCtx, err := u.resolve(ctx)
		if err != nil {
			return err
		}
		return next(resolvedCtx, conn)
	}
}

// resolve resolves the caller's permissions and returns a new context with permissions injected.
// The raw AuthClaims are cleared if set, as to not leak unresolved claims to downstream handlers.
func (u identityInterceptor) resolve(ctx context.Context) (context.Context, error) {
	claims, ok := GetAuthClaims(ctx)
	ctx = WithAuthClaims(
		ctx,
		nil,
	) // Clear raw claims to avoid leaking unresolved claims downstream.
	if !ok {
		// Unauthenticated/public request: nothing to enrich.
		return ctx, nil
	}
	// Stub-org injection applies ONLY to synthetic (no-gateway) claims. With stub
	// enabled behind Kong, a real caller carries Kong-injected claims (Synthetic
	// false) and must resolve their true org via the resolver — forcing the stub
	// org here would break multi-tenant isolation. In local dev without Kong, the
	// auth interceptor fabricates synthetic claims and this short-circuit gives them
	// a deterministic org without the identity service being reachable.
	if u.stub && claims.Synthetic {
		perms := &UserPermissions{
			OrgID:         StubInternalOrgID,
			OrgExternalID: StubOrgExternalID,
			ExternalSub:   claims.Sub,
			OrgRole:       "member",
		}
		return WithUserPermissions(ctx, perms), nil
	}

	perms, err := u.resolver.Resolve(ctx, claims.Sub, claims.OrgID)
	if err != nil {
		// Fail closed, but preserve the resolver's error code so a transient identity
		// outage (Unavailable/DeadlineExceeded, or an open circuit) stays retryable
		// instead of surfacing to the caller as a 401 that logs them out; a genuine
		// auth failure (unknown user) comes through as Unauthenticated.
		code := connect.CodeOf(err)
		if code == connect.CodeUnknown {
			code = connect.CodeUnavailable
		}
		return ctx, connect.NewError(
			code,
			errors.Wrap(err, errors.CodeInternal, "resolve identity"),
		)
	}

	// Retain the caller's external sub on the resolved perms (the raw AuthClaims are
	// cleared above), so a handler can send it as the granter on a grant RPC.
	perms.ExternalSub = claims.Sub
	return WithUserPermissions(ctx, perms), nil
}
