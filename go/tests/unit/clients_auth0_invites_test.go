package unit_test

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/auth0/go-auth0/v2/management"
	auth0core "github.com/auth0/go-auth0/v2/management/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/auth/auth0"
	coreerrors "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// testInviterClient builds a Client over an injected mock Inviter and a real
// circuit breaker, so the invitation/removal surface is exercised black-box.
func testInviterClient(t *testing.T, inv auth0.Inviter) *auth0.Client {
	t.Helper()
	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "auth0-invite-test")
	require.NoError(t, err)
	return auth0.NewWithSeams(nil, nil, nil, inv, nil, cb)
}

// TestAuth0Client_Invitations tests the Auth0 invitation surface through the
// exported Inviter seam: concurrent invite fan-out with per-email results,
// invitation listing, and member removal (each success + error).
//
// Why this test is important:
//   - This is the single Auth0 writer for member onboarding/offboarding; a per-email
//     result mix-up, a dropped list, or a swallowed removal error silently corrupts
//     org membership.
//
// What it tests:
//   - InviteMembers returns a per-email result map (nil on success, mapped error on
//     failure) across a concurrent fan-out.
//   - ListInvitations forwards the org id and returns the invitations; a seam error
//     propagates.
//   - DeleteInvitation forwards the org id + invitation id; a seam error propagates.
//   - RemoveOrganizationMember forwards both ids; a seam error propagates.
func TestAuth0Client_Invitations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("invite members returns a per-email result map", func(t *testing.T) {
		t.Parallel()
		inv := mocks.NewMockInviter(gomock.NewController(t))
		inv.EXPECT().CreateInvitation(gomock.Any(), "org_x", gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, in types.InviteInput) error {
				if in.Email == "bad@corp.test" {
					return stderrors.New("invite rejected")
				}
				return nil
			}).Times(2)

		results := testInviterClient(
			t,
			inv,
		).InviteMembers(ctx, "org_x", []types.InviteInput{
			{Email: "ok@corp.test", InviterName: "Admin"},
			{Email: "bad@corp.test", InviterName: "Admin"},
		})

		require.Len(t, results, 2)
		assert.NoError(t, results["ok@corp.test"], "a successful invite has a nil result")
		assert.Error(
			t,
			results["bad@corp.test"],
			"a failed invite carries its mapped error",
		)
	})

	t.Run("invite members with no invites returns an empty map", func(t *testing.T) {
		t.Parallel()
		inv := mocks.NewMockInviter(gomock.NewController(t))
		results := testInviterClient(t, inv).InviteMembers(ctx, "org_x", nil)
		assert.Empty(t, results)
	})

	t.Run("list invitations forwards org id and returns the list", func(t *testing.T) {
		t.Parallel()
		want := []types.Invitation{{Email: "a@corp.test", RoleIDs: []string{"r1"}}}
		inv := mocks.NewMockInviter(gomock.NewController(t))
		inv.EXPECT().ListInvitations(gomock.Any(), "org_list").Return(want, nil)

		got, err := testInviterClient(t, inv).ListInvitations(ctx, "org_list")

		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("list invitations propagates seam error", func(t *testing.T) {
		t.Parallel()
		inv := mocks.NewMockInviter(gomock.NewController(t))
		inv.EXPECT().ListInvitations(gomock.Any(), "org_list").
			Return(nil, stderrors.New("list down"))

		_, err := testInviterClient(t, inv).ListInvitations(ctx, "org_list")
		require.Error(t, err)
	})

	t.Run("delete invitation forwards org id and invitation id", func(t *testing.T) {
		t.Parallel()
		inv := mocks.NewMockInviter(gomock.NewController(t))
		inv.EXPECT().DeleteInvitation(gomock.Any(), "org_d", "uinv_1").Return(nil)

		require.NoError(t,
			testInviterClient(t, inv).DeleteInvitation(ctx, "org_d", "uinv_1"))
	})

	t.Run("delete invitation propagates seam error", func(t *testing.T) {
		t.Parallel()
		inv := mocks.NewMockInviter(gomock.NewController(t))
		inv.EXPECT().DeleteInvitation(gomock.Any(), "org_d", "uinv_1").
			Return(stderrors.New("revoke down"))

		err := testInviterClient(t, inv).DeleteInvitation(ctx, "org_d", "uinv_1")
		require.Error(t, err)
		assert.NotEqual(t, coreerrors.CodeUnknown, coreerrors.Code(err),
			"a revoke failure must surface as a coded domain error")
	})

	t.Run("remove member forwards both ids", func(t *testing.T) {
		t.Parallel()
		inv := mocks.NewMockInviter(gomock.NewController(t))
		inv.EXPECT().RemoveMember(gomock.Any(), "org_r", "auth0|u1").Return(nil)

		require.NoError(t,
			testInviterClient(t, inv).RemoveOrganizationMember(ctx, "org_r", "auth0|u1"))
	})

	t.Run("remove member propagates seam error", func(t *testing.T) {
		t.Parallel()
		inv := mocks.NewMockInviter(gomock.NewController(t))
		inv.EXPECT().RemoveMember(gomock.Any(), "org_r", "auth0|u1").
			Return(stderrors.New("remove down"))

		require.Error(t,
			testInviterClient(t, inv).RemoveOrganizationMember(ctx, "org_r", "auth0|u1"))
	})
}

// TestAuth0MapErr tests that the Auth0 SDK error taxonomy maps to the correct
// domain error codes on the client's write path.
//
// Why this test is important:
//   - Downstream retry/HTTP-status classification is derived from the domain error
//     code (charter §9.1); mapping a 404 to UPSTREAM or a 400 to INTERNAL would make
//     callers retry a permanent failure or 500 on bad input.
//
// What it tests:
//   - BadRequest→INVALID_INPUT, NotFound→NOT_FOUND, Conflict→CONFLICT,
//     Unauthorized & Forbidden→INTERNAL, and any other error→UPSTREAM.
func TestAuth0MapErr(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	apiErr := func(status int) *auth0core.APIError {
		return auth0core.NewAPIError(status, nil, stderrors.New("sdk failure"))
	}
	cases := []struct {
		name string
		err  error
		want coreerrors.ErrorCode
	}{
		{
			"bad request",
			&management.BadRequestError{APIError: apiErr(400)},
			coreerrors.CodeInvalidInput,
		},
		{
			"not found",
			&management.NotFoundError{APIError: apiErr(404)},
			coreerrors.CodeNotFound,
		},
		{
			"conflict",
			&management.ConflictError{APIError: apiErr(409)},
			coreerrors.CodeConflict,
		},
		{
			"unauthorized",
			&management.UnauthorizedError{APIError: apiErr(401)},
			coreerrors.CodeInternal,
		},
		{
			"forbidden",
			&management.ForbiddenError{APIError: apiErr(403)},
			coreerrors.CodeInternal,
		},
		{"other", stderrors.New("network reset"), coreerrors.CodeUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := mocks.NewMockOrgWriter(gomock.NewController(t))
			w.EXPECT().CreateOrg(gomock.Any(), gomock.Any()).Return("", tc.err)

			_, err := testWriteClient(t, w).
				CreateOrganization(ctx, types.OrgWrite{Slug: "acme"})

			require.Error(t, err)
			assert.Equal(t, tc.want, coreerrors.Code(err),
				"the SDK error type must map to its domain code")
		})
	}
}

// TestAuth0Client_ReadWriteErrorMapping tests that the organization read/update
// surfaces map a seam failure through mapErr (the error paths the happy-path tests
// do not reach).
//
// Why this test is important:
//   - ListOrganizations and UpdateOrganization are on the identity-sync hot path; a
//     seam failure must surface as a coded domain error, not a bare SDK error.
//
// What it tests:
//   - A pager error on ListOrganizations and a writer error on UpdateOrganization
//     both return a non-nil, coded error.
func TestAuth0Client_ReadWriteErrorMapping(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("list organizations maps a pager error", func(t *testing.T) {
		t.Parallel()
		pager := mocks.NewMockOrgPager(gomock.NewController(t))
		pager.EXPECT().ListOrgPage(gomock.Any(), gomock.Any()).
			Return(nil, nil, stderrors.New("orgs endpoint down"))

		_, err := testClient(t, pager, nil).ListOrganizations(ctx)
		require.Error(t, err)
		assert.NotEqual(t, coreerrors.CodeUnknown, coreerrors.Code(err))
	})

	t.Run("update organization maps a writer error", func(t *testing.T) {
		t.Parallel()
		w := mocks.NewMockOrgWriter(gomock.NewController(t))
		w.EXPECT().UpdateOrg(gomock.Any(), gomock.Any()).
			Return(stderrors.New("update down"))

		err := testWriteClient(t, w).
			UpdateOrganization(ctx, types.OrgWrite{ExtID: "org_1", Slug: "acme"})
		require.Error(t, err)
		assert.NotEqual(t, coreerrors.CodeUnknown, coreerrors.Code(err))
	})
}
