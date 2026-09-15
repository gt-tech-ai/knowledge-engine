package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
)

// safeUserProvisioningEnvs is the ALLOW-LIST of environments in which minting Auth0 users is
// permitted. The guard is fail-CLOSED: any env NOT in this set — a prod-like token
// ("prod-us", "production-1", "live"), a typo, or any unrecognized value — is refused, so a
// future or mislabeled production environment can never mint users through this path. The
// empty string is included because SEARCH_ENV is unset in local dev (os.Getenv → ""), where
// user provisioning is intended; a real production deploy sets SEARCH_ENV explicitly.
//
// ⚠️ Only CreateUser consults this — org/invitation mutations (which identity legitimately
// performs in any env) are deliberately NOT guarded here; the seed's own CheckGuards blocks
// destructive prod seed runs. This is the single definition of "an env safe to mint users in".
var safeUserProvisioningEnvs = map[string]struct{}{
	"":            {},
	"dev":         {},
	"development": {},
	"staging":     {},
	"test":        {},
	"local":       {},
}

// prodGuardUserProvider decorates a UserProvider with a fail-closed refusal of user
// provisioning outside an allow-listed non-production environment (charter §6.3: a
// cross-cutting concern wraps business logic, never tangled into it). The embedded
// UserProvider promotes every read/org/create method; only CreateUser is overridden to
// guard, so a future prod can never mint users through this path.
type prodGuardUserProvider struct {
	// UserProvider is the wrapped provider; it promotes every read/org/create
	// method, and only CreateUser is overridden to add the environment guard.
	interfaces.UserProvider
	// env is the current environment name, matched against the non-production
	// allow-list before user provisioning is permitted.
	env string
}

// newProdGuard wraps inner so CreateUser is refused unless env is allow-listed as non-prod.
func newProdGuard(inner interfaces.UserProvider, env string) interfaces.UserProvider {
	return prodGuardUserProvider{UserProvider: inner, env: env}
}

// CreateUser refuses (coded core/errors, no inner call) unless the environment is on the
// non-production allow-list; otherwise it delegates to the wrapped provider.
func (p prodGuardUserProvider) CreateUser(
	ctx context.Context,
	spec types.UserCreate,
) (string, error) {
	env := strings.ToLower(strings.TrimSpace(p.env))
	if _, ok := safeUserProvisioningEnvs[env]; !ok {
		return "", errors.Forbidden(fmt.Sprintf(
			"refusing to create user: SEARCH_ENV=%q is not an allow-listed non-production environment",
			p.env,
		))
	}
	return p.UserProvider.CreateUser(ctx, spec)
}
