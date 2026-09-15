package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
)

// UserProvider extends OrgProvider with user provisioning: minting a login-capable user
// and assigning its (already-resolved) org + clearance role ids against an external auth
// provider (Auth0 today). The Auth0 client and the in-memory stub are its interchangeable
// config-selected backends; the seeder uses it to provision synthetic test identities. It
// composes the core OrgProvider contract (charter §2.3).
type UserProvider interface {
	// OrgProvider supplies the organization + member reads/writes and invitation operations.
	OrgProvider
	// CreateUser mints a login-capable user from spec and assigns spec.RoleIDs, returning
	// the provider user id. The fail-closed prod refusal is applied by a decorator, not
	// this method, so callers depend only on the create-and-assign behaviour.
	CreateUser(ctx context.Context, spec types.UserCreate) (userID string, err error)
}
