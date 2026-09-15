package unit_test

import (
	"context"
	"testing"

	authstub "github.com/gt-tech-ai/knowledge-engine/go/clients/auth/stub"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthStub_OrganizationCRUD tests the in-memory auth stub's organization
// create/update/list/delete flow.
//
// Why this test is important:
//   - The stub backs local dev and tests of the identity flows without a real auth
//     provider; broken CRUD would silently corrupt every org-scoped test.
//
// What it tests:
//   - CreateOrganization returns a non-empty id and the org is then listed.
//   - UpdateOrganization changes the stored name.
//   - DeleteOrganization removes it from the listing.
func TestAuthStub_OrganizationCRUD(t *testing.T) {
	t.Parallel()
	p := authstub.New()
	ctx := context.Background()

	id, err := p.CreateOrganization(ctx, types.OrgWrite{Name: "Acme", Slug: "acme"})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	orgs, err := p.ListOrganizations(ctx)
	require.NoError(t, err)
	require.Len(t, orgs, 1)
	assert.Equal(t, "Acme", orgs[0].Name)

	require.NoError(
		t,
		p.UpdateOrganization(ctx, types.OrgWrite{ExtID: id, Name: "Acme2", Slug: "acme"}),
	)
	orgs, err = p.ListOrganizations(ctx)
	require.NoError(t, err)
	assert.Equal(t, "Acme2", orgs[0].Name)

	require.NoError(t, p.DeleteOrganization(ctx, id))
	orgs, err = p.ListOrganizations(ctx)
	require.NoError(t, err)
	assert.Empty(t, orgs)
}

// TestAuthStub_MembersAndInvitations tests the stub's member and invitation flows.
//
// Why this test is important:
//   - Member removal and invitation recording are exercised by identity-flow tests;
//     a broken stub would make those tests assert against wrong membership state.
//
// What it tests:
//   - InviteMembers records one invitation per email (all succeed) and ListInvitations returns them,
//     each stamped with a distinct id and a created/expires window (the shape Auth0 returns, and what
//     a revoke needs to address one invitation).
//   - DeleteInvitation revokes exactly the addressed invitation and is a safe no-op for an unknown id.
//   - ListOrganizationMembers returns the empty set for an org with no members.
//   - RemoveOrganizationMember is a no-op safe to call on an org with no such member.
func TestAuthStub_MembersAndInvitations(t *testing.T) {
	t.Parallel()
	p := authstub.New()
	ctx := context.Background()

	results := p.InviteMembers(ctx, "org-1", []types.InviteInput{
		{Email: "a@example.com", RoleIDs: []string{"r1"}},
		{Email: "b@example.com"},
	})
	assert.Len(t, results, 2)
	assert.NoError(t, results["a@example.com"])
	assert.NoError(t, results["b@example.com"])

	invites, err := p.ListInvitations(ctx, "org-1")
	require.NoError(t, err)
	require.Len(t, invites, 2)
	assert.NotEqual(t, invites[0].ID, invites[1].ID,
		"each recorded invitation gets its own id, so a revoke can address exactly one")
	for _, inv := range invites {
		assert.NotEmpty(t, inv.ID)
		assert.False(
			t,
			inv.CreatedAt.IsZero(),
			"a listed invitation carries when it was created",
		)
		assert.True(t, inv.ExpiresAt.After(inv.CreatedAt),
			"a listed invitation expires after it was created")
	}

	// A revoke removes only the addressed invitation; an unknown id is a safe no-op.
	require.NoError(t, p.DeleteInvitation(ctx, "org-1", invites[0].ID))
	require.NoError(t, p.DeleteInvitation(ctx, "org-1", "invitation_stub_absent"))
	remaining, err := p.ListInvitations(ctx, "org-1")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, invites[1].ID, remaining[0].ID)

	members, err := p.ListOrganizationMembers(ctx, "org-1")
	require.NoError(t, err)
	assert.Empty(t, members)

	// Removing a non-existent member is safe (no members recorded for this org).
	require.NoError(t, p.RemoveOrganizationMember(ctx, "org-1", "user-x"))
}

// TestAuthStub_CreateUser tests that the in-memory stub provisions a deterministic,
// login-shaped user id without a live Auth0 tenant.
//
// Why this test is important:
//   - The stub is the no-creds seed path (39.7): it must satisfy UserProvider and return a
//     stable id so seeding runs offline without reaching Auth0.
//
// What it tests:
//   - CreateUser returns a non-empty, deterministic "auth0|user_stub_N" id (no I/O, no error).
func TestAuthStub_CreateUser(t *testing.T) {
	t.Parallel()

	p := authstub.New()
	id, err := p.CreateUser(context.Background(), types.UserCreate{
		Email:      "persona@example.com",
		Connection: "Username-Password-Authentication",
		RoleIDs:    []string{"rol_org_admin", "rol_clearance_public"},
	})
	require.NoError(t, err)
	assert.Equal(
		t,
		"auth0|user_stub_1",
		id,
		"the first stub user gets a deterministic id",
	)
}
