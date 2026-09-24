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

// WithUserPermissions stores resolved user permissions in the context (the
// *UserPermissions principal; see WithPrincipal).
func WithUserPermissions(
	ctx context.Context,
	userPerms *UserPermissions,
) context.Context {
	return WithPrincipal(ctx, userPerms)
}

// GetUserPermissions retrieves resolved user permissions from the context (the
// *UserPermissions principal; see PrincipalFrom).
func GetUserPermissions(ctx context.Context) (*UserPermissions, bool) {
	return PrincipalFrom[*UserPermissions](ctx)
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

// NewIdentityInterceptor returns the identity enrichment interceptor: a principal
// interceptor (NewPrincipalInterceptor) whose principal is *UserPermissions,
// resolved from the caller's (sub, org external id) by resolver.
//
// Behavior (per RPC, unary or streaming):
//   - no AuthClaims in context (unauthenticated/public request) → pass through
//   - stub mode (auth.stub=true) with synthetic (no-gateway) claims → a deterministic
//     stub context, no resolver call. Real claims always resolve: forcing the stub
//     org on a caller behind the gateway would break multi-tenant isolation.
//   - otherwise → resolve and enrich; a resolver error fails the request CLOSED,
//     keeping its code so a transient identity outage stays retryable instead of
//     surfacing as a 401 that logs the caller out
//
// The caller's external sub is retained on the resolved perms (the raw AuthClaims
// are cleared), so a handler can send it as the granter on a grant RPC.
func NewIdentityInterceptor(
	resolver IdentityResolver,
	stub bool,
) connect.Interceptor {
	resolve := func(ctx context.Context, claims *AuthClaims) (*UserPermissions, error) {
		perms, err := resolver.Resolve(ctx, claims.Sub, claims.OrgID)
		if err != nil {
			return nil, err
		}
		perms.ExternalSub = claims.Sub
		return perms, nil
	}
	var stubPerms func(*AuthClaims) *UserPermissions
	if stub {
		stubPerms = func(claims *AuthClaims) *UserPermissions {
			return &UserPermissions{
				OrgID:         StubInternalOrgID,
				OrgExternalID: StubOrgExternalID,
				ExternalSub:   claims.Sub,
				OrgRole:       "member",
			}
		}
	}
	return NewPrincipalInterceptor(resolve, stubPerms)
}
