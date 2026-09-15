package auth0

import (
	"context"
	"time"

	"github.com/auth0/go-auth0/v2/management"
	mgmclient "github.com/auth0/go-auth0/v2/management/client"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
)

// Client satisfies the core UserProvider contract (OrgProvider + CreateUser).
var _ interfaces.UserProvider = (*Client)(nil)

// UserProvisioner mints a login-capable user and assigns tenant role ids to it. The SDK
// adapter implements it against go-auth0; a generated mock drives it in tests.
// SDK seam (charter §2.3): abstracts the go-auth0 SDK for user create + role assignment.
type UserProvisioner interface {
	// CreateUser creates the user and returns the provider user id.
	CreateUser(ctx context.Context, spec types.UserCreate) (userID string, err error)
	// AssignRoles assigns the given tenant role ids to the user (a no-op for none).
	AssignRoles(ctx context.Context, userID string, roleIDs []string) error
	// UserIDByEmail returns the provider id of the user with email, or "" (nil error) when no
	// such user exists yet. Used to resolve the user's REAL id after create instead of
	// reconstructing auth0|<UserID> — the tenant does not necessarily mint the id we asked for.
	UserIDByEmail(ctx context.Context, email string) (userID string, err error)
}

// SeamOption customises the SDK-adapter seams a Client is built with, so a test can inject
// a generated mock for a specific seam without a live Auth0 tenant.
type SeamOption func(*Client)

// WithUserProvisioner injects the user-provisioning seam — the real sdkUserProvisioner in
// production (wired by New), a generated mock in tests.
func WithUserProvisioner(p UserProvisioner) SeamOption {
	return func(c *Client) { c.userProvisioner = p }
}

// WithAssignRetry overrides the role-assignment propagation-retry bounds (see
// Client.assignRetryMax). Tests use it to shrink the backoff so the create→assign
// eventual-consistency retry can be exercised without real-time sleeps.
func WithAssignRetry(maxRetries int, base time.Duration) SeamOption {
	return func(c *Client) {
		c.assignRetryMax = maxRetries
		c.assignRetryBase = base
	}
}

// CreateUser mints a login-capable user from spec and assigns spec.RoleIDs (the caller's
// already-resolved org + clearance role ids), returning the provider user id. It runs
// through the resilience stack and maps failures to coded core/errors. The fail-closed
// prod refusal lives in the prodGuardUserProvider decorator, not here.
//
// Idempotency: a Conflict (the user already exists) is tolerated — the user's real id is then
// resolved by email and roles are (re)assigned anyway, so a prior run that created the user but
// failed AssignRoles converges instead of leaving the user permanently role-less (Auth0 role
// assignment is idempotent). The id is resolved by email, NOT reconstructed as auth0|<UserID>:
// the tenant does not necessarily mint the user_id we requested, and a reconstructed id 404s
// (inexistent_user) on role assignment — the failure this replaces.
func (c *Client) CreateUser(ctx context.Context, spec types.UserCreate) (string, error) {
	if c.userProvisioner == nil {
		return "", errors.New(
			errors.CodeInternal,
			"auth0 user provisioner not configured",
		)
	}
	var userID string
	err := c.run(ctx, "create_user", false, func(cctx context.Context) error {
		_, createErr := c.userProvisioner.CreateUser(cctx, spec)
		// A Conflict means the user already exists (idempotent re-run) — tolerate it and resolve
		// the existing user below; any other create error is fatal.
		if createErr != nil && !isConflictErr(createErr) {
			return createErr
		}
		// Resolve the user's REAL provider id by email (retrying the create→read propagation lag)
		// rather than trusting a reconstructed auth0|<UserID>, then assign roles against it.
		id, err := c.resolveUserIDByEmail(cctx, spec.Email)
		if err != nil {
			return err
		}
		userID = id
		return c.assignRolesWithPropagationRetry(cctx, userID, spec.RoleIDs)
	})
	if err != nil {
		return "", mapErr(err, "create user")
	}
	return userID, nil
}

// resolveUserIDByEmail returns the provider id of the user with email, retrying the lookup on an
// empty result with the same bounded backoff as role assignment — a user Auth0 has just created is
// not immediately visible to the users-by-email read (the create→read propagation lag). An empty
// result after the bound is a hard failure (the create reported success/conflict, so the user must
// exist). Requires the read:users Management-API scope.
func (c *Client) resolveUserIDByEmail(ctx context.Context, email string) (string, error) {
	delay := c.assignRetryBase
	for attempt := 0; ; attempt++ {
		id, err := c.userProvisioner.UserIDByEmail(ctx, email)
		if err != nil {
			return "", err
		}
		if id != "" {
			return id, nil
		}
		if attempt >= c.assignRetryMax {
			return "", errors.New(
				errors.CodeUpstream,
				"user not found by email after create: "+email,
			)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// assignRolesWithPropagationRetry assigns roles, retrying on a NotFound (Auth0's
// inexistent_user) with bounded exponential backoff. A user Auth0 has just created is
// not immediately visible to POST /users/{id}/roles — the create→assign propagation lag
// that fails persona seeding — so a role assignment against a fresh user can transiently
// 404 until the tenant converges. We retry the WRITE rather than polling GET /users/{id}
// because the seed M2M client is granted update:users but not read:users. Non-404 errors
// (bad request, auth, conflict) fail fast. A genuinely missing role also 404s and is retried
// up to the bound before surfacing — acceptable and non-fatal, since role ids are
// config-validated (auth.auth0_roles), so in practice only the user-propagation case is hit.
func (c *Client) assignRolesWithPropagationRetry(
	ctx context.Context,
	userID string,
	roleIDs []string,
) error {
	delay := c.assignRetryBase
	for attempt := 0; ; attempt++ {
		err := c.userProvisioner.AssignRoles(ctx, userID, roleIDs)
		if err == nil || !isNotFoundErr(err) || attempt >= c.assignRetryMax {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// sdkUserProvisioner adapts the go-auth0 client to UserProvisioner.
type sdkUserProvisioner struct {
	// client is the go-auth0 management SDK client.
	client *mgmclient.Management
}

// CreateUser creates the user in the given database connection (email pre-verified per
// spec so synthetic personas receive no verification mail) and returns its Auth0 user id.
func (s sdkUserProvisioner) CreateUser(
	ctx context.Context,
	spec types.UserCreate,
) (string, error) {
	req := &management.CreateUserRequestContent{}
	req.SetConnection(spec.Connection)
	if spec.UserID != "" {
		userID := spec.UserID
		req.SetUserID(&userID)
	}
	if spec.Email != "" {
		email := spec.Email
		req.SetEmail(&email)
	}
	if spec.Password != "" {
		password := spec.Password
		req.SetPassword(&password)
	}
	verified := spec.EmailVerified
	req.SetEmailVerified(&verified)

	resp, err := s.client.Users.Create(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.GetUserID(), nil
}

// AssignRoles assigns the tenant role ids to the user (POST /users/{id}/roles).
func (s sdkUserProvisioner) AssignRoles(
	ctx context.Context,
	userID string,
	roleIDs []string,
) error {
	if len(roleIDs) == 0 {
		return nil
	}
	req := &management.AssignUserRolesRequestContent{}
	req.SetRoles(roleIDs)
	return s.client.Users.Roles.Assign(ctx, userID, req)
}

// UserIDByEmail returns the provider id of the first user with email (GET /users-by-email), or ""
// (nil error) when none exists yet. Personas carry unique emails, so the match is 1:1. Requires
// the read:users Management-API scope.
func (s sdkUserProvisioner) UserIDByEmail(
	ctx context.Context,
	email string,
) (string, error) {
	req := &management.ListUsersByEmailRequestParameters{}
	req.SetEmail(email)
	users, err := s.client.Users.ListUsersByEmail(ctx, req)
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if id := u.GetUserID(); id != "" {
			return id, nil
		}
	}
	return "", nil
}
