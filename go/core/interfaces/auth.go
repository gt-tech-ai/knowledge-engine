package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
)

// AuthProvider handles authentication and authorization.
//
// Phase 1: JWT validation, role checking, tenant context extraction.
// Phase 2+: RBAC policies, fine-grained permissions, attribute-based access control.
type AuthProvider interface {
	// ValidateToken validates a JWT/bearer token and returns the authenticated identity.
	ValidateToken(ctx context.Context, token string) (*AuthIdentity, error)

	// HasRole checks if the authenticated user has the specified role.
	HasRole(ctx context.Context, role string) bool

	// HasPermission checks if the authenticated user has a specific permission
	// on a resource.
	HasPermission(ctx context.Context, permission, resource string) bool

	// TenantContext extracts multi-tenant context from the request.
	TenantContext(ctx context.Context) (*types.TenantContext, error)
}

// ServiceTokenValidator validates a service-to-service bearer token and returns the
// identity of the calling service. It is the swappable Strategy behind the internal
// service-auth interceptor: Phase 1 ships a static per-caller token
// validator; Phase 2 can swap in a workload-identity validator (K8s SA projected
// tokens / mesh mTLS) without touching the interceptor or its call sites.
type ServiceTokenValidator interface {
	// Validate returns the calling service's name for a valid token, or ok=false when
	// the token matches no known caller.
	Validate(token string) (caller string, ok bool)
}

// AuthIdentity represents an authenticated user/service identity.
type AuthIdentity struct {
	// UserID is the unique identifier of the authenticated user.
	UserID types.ID `json:"user_id"`

	// OrgID is the organization the user belongs to.
	OrgID types.ID `json:"org_id"`

	// Email is the user's email address.
	Email string `json:"email"`

	// ClearanceLevel is the user's security clearance level.
	ClearanceLevel string `json:"clearance_level"`

	// Roles is the set of roles assigned to the user.
	Roles []string `json:"roles"`
}
