package unit_test

import (
	"context"
	stderrors "errors"
	"testing"
	"time"

	"github.com/auth0/go-auth0/v2/management"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/auth/auth0"
	coreerrors "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// testUserClient builds a Client over an injected mock UserProvisioner seam and a real
// circuit breaker, so the user-provisioning surface is exercised black-box.
func testUserClient(t *testing.T, p auth0.UserProvisioner) *auth0.Client {
	t.Helper()
	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "auth0-user-test")
	require.NoError(t, err)
	return auth0.NewWithSeams(nil, nil, nil, nil, nil, cb, auth0.WithUserProvisioner(p))
}

// TestCreateUser_ResolvesRealIDByEmailThenAssignsRoles tests that CreateUser resolves the user's
// REAL provider id by email after create and assigns roles against THAT id (not the create
// response, not a reconstructed auth0|<UserID>).
//
// Why this test is important:
//   - This is the primitive the seeder (39.7) uses to provision test identities, and the fix for
//
// the staging seed-personas failure: the tenant does not necessarily mint the
//
//	user_id we request, so assigning roles against a reconstructed id 404s (inexistent_user).
//	Resolving the real id by email is what makes role assignment (and idempotent re-runs) work.
//
// What it tests:
//   - CreateUser calls the seam's CreateUser, resolves the id via UserIDByEmail, then AssignRoles
//     against the RESOLVED id with exactly spec.RoleIDs — returning the resolved id, not the
//     (deliberately different) create-response id.
func TestCreateUser_ResolvesRealIDByEmailThenAssignsRoles(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	prov := mocks.NewMockUserProvisioner(ctrl)

	orgRoleID := "rol_org_admin"
	clearanceID := "rol_clearance_internal"
	spec := types.UserCreate{
		Email:         "persona@example.com",
		Connection:    "Username-Password-Authentication",
		Password:      "Temp-Passw0rd!",
		EmailVerified: true,
		RoleIDs:       []string{orgRoleID, clearanceID},
	}

	gomock.InOrder(
		// The create-response id is deliberately different from the resolved id: the client must
		// use the id resolved by email, proving it never trusts the create response / reconstruction.
		prov.EXPECT().
			CreateUser(gomock.Any(), spec).
			Return("auth0|ignored-create-id", nil),
		prov.EXPECT().
			UserIDByEmail(gomock.Any(), "persona@example.com").
			Return("auth0|realuser", nil),
		prov.EXPECT().
			AssignRoles(gomock.Any(), "auth0|realuser", []string{orgRoleID, clearanceID}).
			Return(nil),
	)

	id, err := testUserClient(t, prov).CreateUser(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(
		t,
		"auth0|realuser",
		id,
		"the id resolved by email is used, not the create response",
	)
}

// TestCreateUser_ErrorIsCoded tests that a non-conflict SDK create failure crosses the boundary as
// a coded core/errors error and never attempts the email lookup or role assignment.
//
// Why this test is important:
//   - Charter §9.1: every error crossing a boundary carries a code so retry/HTTP-status
//     classification is derivable. And a real create failure must abort — not proceed to resolve
//     a user that was never created.
//
// What it tests:
//   - When the seam's CreateUser fails with a non-conflict error, CreateUser returns a coded
//     (CodeUpstream) error; UserIDByEmail and AssignRoles are never called.
func TestCreateUser_ErrorIsCoded(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	prov := mocks.NewMockUserProvisioner(ctrl)

	spec := types.UserCreate{
		Email:      "x@example.com",
		Connection: "c",
		RoleIDs:    []string{"rol_x"},
	}
	prov.EXPECT().CreateUser(gomock.Any(), spec).Return("", stderrors.New("auth0 boom"))
	// UserIDByEmail + AssignRoles must NOT be called after a non-conflict create failure.

	_, err := testUserClient(t, prov).CreateUser(context.Background(), spec)
	require.Error(t, err)
	assert.Equal(t, coreerrors.CodeUpstream, coreerrors.Code(err),
		"an SDK failure must surface as a coded core/errors error")
}

// TestCreateUser_ConflictResolvesAndReassignsRoles tests that an "already exists" (409) conflict is
// idempotent — the existing user's real id is resolved by email and roles are (re)assigned.
//
// Why this test is important:
//   - A prior run may have created the user but failed AssignRoles. If a re-run's create-conflict
//     short-circuited, the persona would be permanently role-less — its JWT lacking the
//     org/clearance roles the seed exists to set. Resolving the existing user by email makes the
//     re-run converge.
//
// What it tests:
//   - When the seam's CreateUser returns a ConflictError, the client tolerates it, resolves the id
//     via UserIDByEmail, and calls AssignRoles against the resolved id — returning it with no error.
func TestCreateUser_ConflictResolvesAndReassignsRoles(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	prov := mocks.NewMockUserProvisioner(ctrl)

	spec := types.UserCreate{
		Email:      "p@example.com",
		Connection: "Username-Password-Authentication",
		Password:   "pw",
		UserID:     "persona-x",
		RoleIDs:    []string{"rol_a", "rol_b"},
	}
	gomock.InOrder(
		prov.EXPECT().
			CreateUser(gomock.Any(), spec).
			Return("", &management.ConflictError{}),
		prov.EXPECT().
			UserIDByEmail(gomock.Any(), "p@example.com").
			Return("auth0|persona-x", nil),
		prov.EXPECT().
			AssignRoles(gomock.Any(), "auth0|persona-x", []string{"rol_a", "rol_b"}).
			Return(nil),
	)

	id, err := testUserClient(t, prov).CreateUser(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "auth0|persona-x", id)
}

// TestCreateUser_RetriesEmailLookupOnFreshUserPropagation tests that an empty users-by-email result
// — the just-created user not yet visible to the read — is retried until it resolves.
//
// Why this test is important:
//   - This is the staging seed-personas failure: a user Auth0 has just created is not
//     immediately queryable. Without retrying the lookup, the seed fails to resolve the id and
//     personas never receive their org/clearance roles.
//
// What it tests:
//   - When UserIDByEmail returns "" (not found) twice then the id, CreateUser retries the lookup,
//     then assigns roles and returns the id with no error.
func TestCreateUser_RetriesEmailLookupOnFreshUserPropagation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	prov := mocks.NewMockUserProvisioner(ctrl)

	spec := types.UserCreate{
		Email:      "p@example.com",
		Connection: "Username-Password-Authentication",
		Password:   "pw",
		UserID:     "persona-x",
		RoleIDs:    []string{"rol_a", "rol_b"},
	}
	gomock.InOrder(
		prov.EXPECT().CreateUser(gomock.Any(), spec).Return("auth0|persona-x", nil),
		prov.EXPECT().UserIDByEmail(gomock.Any(), "p@example.com").Return("", nil),
		prov.EXPECT().UserIDByEmail(gomock.Any(), "p@example.com").Return("", nil),
		prov.EXPECT().
			UserIDByEmail(gomock.Any(), "p@example.com").
			Return("auth0|persona-x", nil),
		prov.EXPECT().
			AssignRoles(gomock.Any(), "auth0|persona-x", []string{"rol_a", "rol_b"}).
			Return(nil),
	)

	cb, err := circuitbreaker.New(
		circuitbreaker.KindGoBreaker,
		"auth0-user-resolve-retry-test",
	)
	require.NoError(t, err)
	client := auth0.NewWithSeams(nil, nil, nil, nil, nil, cb,
		auth0.WithUserProvisioner(prov), auth0.WithAssignRetry(5, time.Millisecond))

	id, err := client.CreateUser(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "auth0|persona-x", id)
}

// TestCreateUser_EmailLookupNeverResolvesExhaustsRetries tests that a users-by-email lookup that
// never returns the user surfaces a coded error after the bounded retry, rather than looping.
//
// Why this test is important:
//   - The propagation retry MUST be bounded: a user that never appears has to fail loudly so the
//     job's own retry/backoff takes over instead of hanging.
//
// What it tests:
//   - With AssignRetry(2, …), UserIDByEmail is attempted 1+2 = 3 times, all empty, and CreateUser
//     returns a coded (CodeUpstream) error; AssignRoles is never called.
func TestCreateUser_EmailLookupNeverResolvesExhaustsRetries(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	prov := mocks.NewMockUserProvisioner(ctrl)

	spec := types.UserCreate{
		Email:      "p@example.com",
		Connection: "Username-Password-Authentication",
		Password:   "pw",
		UserID:     "persona-y",
		RoleIDs:    []string{"rol_a"},
	}
	prov.EXPECT().CreateUser(gomock.Any(), spec).Return("auth0|persona-y", nil)
	prov.EXPECT().UserIDByEmail(gomock.Any(), "p@example.com").Return("", nil).Times(3)

	cb, err := circuitbreaker.New(
		circuitbreaker.KindGoBreaker,
		"auth0-user-resolve-exhaust-test",
	)
	require.NoError(t, err)
	client := auth0.NewWithSeams(nil, nil, nil, nil, nil, cb,
		auth0.WithUserProvisioner(prov), auth0.WithAssignRetry(2, time.Millisecond))

	_, err = client.CreateUser(context.Background(), spec)
	require.Error(t, err)
	assert.Equal(
		t,
		coreerrors.CodeUpstream,
		coreerrors.Code(err),
		"a user that never resolves by email surfaces a coded error after the retry bound",
	)
}

// TestCreateUser_RetriesRoleAssignOnFreshUserPropagation tests that a role assignment that
// transiently 404s (the resolved user still propagating to the roles endpoint) is retried until it
// succeeds, so CreateUser converges instead of failing on the residual assign race.
//
// Why this test is important:
//   - Resolving the id by email confirms the user exists, but POST /users/{id}/roles can still
//     briefly 404 on a fresh user; the assign-side retry keeps the seed converging.
//
// What it tests:
//   - When AssignRoles returns NotFound twice then succeeds, CreateUser retries and returns the id.
func TestCreateUser_RetriesRoleAssignOnFreshUserPropagation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	prov := mocks.NewMockUserProvisioner(ctrl)

	spec := types.UserCreate{
		Email:      "p@example.com",
		Connection: "Username-Password-Authentication",
		Password:   "pw",
		UserID:     "persona-x",
		RoleIDs:    []string{"rol_a", "rol_b"},
	}
	gomock.InOrder(
		prov.EXPECT().CreateUser(gomock.Any(), spec).Return("auth0|persona-x", nil),
		prov.EXPECT().
			UserIDByEmail(gomock.Any(), "p@example.com").
			Return("auth0|persona-x", nil),
		prov.EXPECT().
			AssignRoles(gomock.Any(), "auth0|persona-x", []string{"rol_a", "rol_b"}).
			Return(&management.NotFoundError{}),
		prov.EXPECT().
			AssignRoles(gomock.Any(), "auth0|persona-x", []string{"rol_a", "rol_b"}).
			Return(&management.NotFoundError{}),
		prov.EXPECT().
			AssignRoles(gomock.Any(), "auth0|persona-x", []string{"rol_a", "rol_b"}).
			Return(nil),
	)

	cb, err := circuitbreaker.New(
		circuitbreaker.KindGoBreaker,
		"auth0-user-assign-retry-test",
	)
	require.NoError(t, err)
	client := auth0.NewWithSeams(nil, nil, nil, nil, nil, cb,
		auth0.WithUserProvisioner(prov), auth0.WithAssignRetry(5, time.Millisecond))

	id, err := client.CreateUser(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "auth0|persona-x", id)
}
