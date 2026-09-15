package unit_test

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/auth/auth0"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

func strptr(s string) *string { return &s }

// testClient builds a Client over injected mock pagers and a real circuit breaker,
// wiring a default role lister that returns no roles for any member so the
// member-read subtests need no role expectations.
func testClient(
	t *testing.T,
	orgs auth0.OrgPager,
	members auth0.MemberPager,
) *auth0.Client {
	t.Helper()
	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "auth0-test")
	require.NoError(t, err)
	roles := mocks.NewMockMemberRoleLister(gomock.NewController(t))
	roles.EXPECT().
		ListMemberRoles(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, nil).
		AnyTimes()
	return auth0.NewWithSeams(orgs, members, nil, nil, roles, cb)
}

// testWriteClient builds a Client over an injected mock writer and a real breaker.
func testWriteClient(t *testing.T, w auth0.OrgWriter) *auth0.Client {
	t.Helper()
	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "auth0-write-test")
	require.NoError(t, err)
	return auth0.NewWithSeams(nil, nil, w, nil, nil, cb)
}

// memberPageOf builds a page of n valid raw members with the given id prefix.
func memberPageOf(prefix string, n int) []auth0.RawMember {
	page := make([]auth0.RawMember, 0, n)
	for i := 0; i < n; i++ {
		id := prefix + strconv.Itoa(i)
		page = append(page, auth0.RawMember{
			Sub:   strptr("auth0|" + id),
			Email: strptr(id + "@corp.test"),
			Name:  strptr("User " + id),
		})
	}
	return page
}

// TestAuth0New_BuildsProvider tests that New constructs an Auth0-backed
// OrgProvider offline (the client-credentials token is fetched lazily on the first
// API call), so a wiring regression fails at construction rather than at first use.
//
// Why this test is important:
//   - New is the production constructor the auth factory selects for KindAuth0; if
//     it fails to build, every Auth0 read/write nil-derefs at first use.
//
// What it tests:
//   - New returns a non-nil Client (which satisfies interfaces.OrgProvider) and no
//     error for valid credentials.
func TestAuth0New_BuildsProvider(t *testing.T) {
	t.Parallel()
	c, err := auth0.New(context.Background(), auth0.Params{
		Domain:       "tenant.us.auth0.com",
		ClientID:     "cid",
		ClientSecret: "secret",
	})
	require.NoError(t, err)
	require.NotNil(t, c)
}

// TestAuth0Client_Reads tests the Auth0 read client's pagination, nil-skip, and
// bounded role fan-out through its exported SDK seams (black-box, no live tenant).
//
// Why this test is important:
//   - This client is the single Auth0 reader for identity sync; a pagination,
//     nil-skip, or role-fan-out regression silently truncates or corrupts the synced
//     identity set.
//
// What it tests:
//   - Members: all pages walked; missing sub/email dropped; empty → empty slice;
//     fetch error propagated; stuck cursor terminates; roles fetched per member,
//     carried on, concurrently but bounded and in member order; role error fails read.
//   - Organizations: all pages walked and mapped; missing id dropped.
func TestAuth0Client_Reads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("members paginate all pages", func(t *testing.T) {
		t.Parallel()
		pager := mocks.NewMockMemberPager(gomock.NewController(t))
		gomock.InOrder(
			pager.EXPECT().ListMemberPage(gomock.Any(), "org_big", gomock.Any()).
				Return(memberPageOf("a", 50), strptr("1"), nil),
			pager.EXPECT().ListMemberPage(gomock.Any(), "org_big", gomock.Any()).
				Return(memberPageOf("b", 50), strptr("2"), nil),
			pager.EXPECT().ListMemberPage(gomock.Any(), "org_big", gomock.Any()).
				Return(memberPageOf("c", 20), nil, nil),
		)

		got, err := testClient(t, nil, pager).ListOrganizationMembers(ctx, "org_big")

		require.NoError(t, err)
		require.Len(t, got, 120)
		assert.Equal(t, "auth0|a0", got[0].Sub)
		assert.Equal(t, "auth0|c19", got[119].Sub)
	})

	t.Run("members skip missing sub or email", func(t *testing.T) {
		t.Parallel()
		pager := mocks.NewMockMemberPager(gomock.NewController(t))
		pager.EXPECT().ListMemberPage(gomock.Any(), "org_partial", gomock.Any()).
			Return([]auth0.RawMember{
				{Sub: nil, Email: strptr("nosub@corp.test")},
				{Sub: strptr("auth0|noemail"), Email: nil},
				{
					Sub:   strptr("auth0|ok"),
					Email: strptr("ok@corp.test"),
					Name:  strptr("OK"),
				},
			}, nil, nil)

		got, err := testClient(t, nil, pager).ListOrganizationMembers(ctx, "org_partial")

		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "auth0|ok", got[0].Sub)
	})

	t.Run("members empty returns empty slice", func(t *testing.T) {
		t.Parallel()
		pager := mocks.NewMockMemberPager(gomock.NewController(t))
		pager.EXPECT().ListMemberPage(gomock.Any(), "org_empty", gomock.Any()).
			Return([]auth0.RawMember{}, nil, nil)

		got, err := testClient(t, nil, pager).ListOrganizationMembers(ctx, "org_empty")

		require.NoError(t, err, "empty org should not error at the client layer")
		assert.Empty(t, got)
	})

	t.Run("members carry their auth0 roles", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		pager := mocks.NewMockMemberPager(ctrl)
		pager.EXPECT().ListMemberPage(gomock.Any(), "org_roles", gomock.Any()).
			Return([]auth0.RawMember{
				{
					Sub:   strptr("auth0|admin"),
					Email: strptr("admin@corp.test"),
					Name:  strptr("Admin"),
				},
				{
					Sub:   strptr("auth0|plain"),
					Email: strptr("plain@corp.test"),
					Name:  strptr("Plain"),
				},
			}, nil, nil)
		roles := mocks.NewMockMemberRoleLister(ctrl)
		roles.EXPECT().ListMemberRoles(gomock.Any(), "org_roles", "auth0|admin").
			Return([]string{"org-admin", "member"}, nil)
		roles.EXPECT().ListMemberRoles(gomock.Any(), "org_roles", "auth0|plain").
			Return([]string{"member"}, nil)
		cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "auth0-roles-test")
		require.NoError(t, err)

		got, err := auth0.NewWithSeams(nil, pager, nil, nil, roles, cb).
			ListOrganizationMembers(ctx, "org_roles")

		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, []string{"org-admin", "member"}, got[0].Roles)
		assert.Equal(t, []string{"member"}, got[1].Roles)
	})

	t.Run(
		"member role fetches run concurrently, bounded, and stay ordered",
		func(t *testing.T) {
			t.Parallel()
			const n = 24
			ctrl := gomock.NewController(t)
			raws := make([]auth0.RawMember, n)
			for i := range raws {
				sub := "auth0|" + strconv.Itoa(i)
				raws[i] = auth0.RawMember{
					Sub:   strptr(sub),
					Email: strptr(sub + "@corp.test"),
				}
			}
			pager := mocks.NewMockMemberPager(ctrl)
			pager.EXPECT().ListMemberPage(gomock.Any(), "org_conc", gomock.Any()).
				Return(raws, nil, nil)

			var inFlight, maxInFlight atomic.Int32
			roles := mocks.NewMockMemberRoleLister(ctrl)
			roles.EXPECT().
				ListMemberRoles(gomock.Any(), "org_conc", gomock.Any()).
				DoAndReturn(func(_ context.Context, _, sub string) ([]string, error) {
					cur := inFlight.Add(1)
					for {
						m := maxInFlight.Load()
						if cur <= m || maxInFlight.CompareAndSwap(m, cur) {
							break
						}
					}
					time.Sleep(10 * time.Millisecond)
					inFlight.Add(-1)
					return []string{"role-" + sub}, nil
				}).Times(n)
			cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "auth0-conc-test")
			require.NoError(t, err)

			got, err := auth0.NewWithSeams(nil, pager, nil, nil, roles, cb).
				ListOrganizationMembers(ctx, "org_conc")

			require.NoError(t, err)
			require.Len(t, got, n)
			assert.Greater(t, maxInFlight.Load(), int32(1),
				"per-member role fetches must run concurrently, not one at a time")
			assert.LessOrEqual(t, maxInFlight.Load(), int32(12),
				"per-member role fetch concurrency must be bounded at 12")
			for i := range got {
				assert.Equal(t, []string{"role-auth0|" + strconv.Itoa(i)}, got[i].Roles,
					"result must stay positionally aligned with the member list")
			}
		},
	)

	t.Run("members propagate role-fetch error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		pager := mocks.NewMockMemberPager(ctrl)
		pager.EXPECT().ListMemberPage(gomock.Any(), "org_re", gomock.Any()).
			Return([]auth0.RawMember{
				{Sub: strptr("auth0|ok"), Email: strptr("ok@corp.test")},
			}, nil, nil)
		roles := mocks.NewMockMemberRoleLister(ctrl)
		roles.EXPECT().ListMemberRoles(gomock.Any(), "org_re", "auth0|ok").
			Return(nil, errors.New("roles endpoint down"))
		cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "auth0-re-test")
		require.NoError(t, err)

		_, err = auth0.NewWithSeams(nil, pager, nil, nil, roles, cb).
			ListOrganizationMembers(ctx, "org_re")

		require.Error(t, err, "a role-fetch failure must fail the member read")
	})

	t.Run("members propagate fetch error", func(t *testing.T) {
		t.Parallel()
		pager := mocks.NewMockMemberPager(gomock.NewController(t))
		pager.EXPECT().ListMemberPage(gomock.Any(), "org_x", gomock.Any()).
			Return(nil, nil, errors.New("auth0 down"))

		_, err := testClient(t, nil, pager).ListOrganizationMembers(ctx, "org_x")

		require.Error(t, err, "want error when the fetch fails")
	})

	t.Run("members terminate on stuck cursor", func(t *testing.T) {
		t.Parallel()
		stuck := strptr("same-cursor")
		pager := mocks.NewMockMemberPager(gomock.NewController(t))
		gomock.InOrder(
			pager.EXPECT().ListMemberPage(gomock.Any(), "org_stuck", gomock.Any()).
				Return(memberPageOf("z", 3), stuck, nil),
			pager.EXPECT().ListMemberPage(gomock.Any(), "org_stuck", gomock.Any()).
				Return(memberPageOf("z", 3), stuck, nil),
		)

		got, err := testClient(t, nil, pager).ListOrganizationMembers(ctx, "org_stuck")

		require.NoError(t, err)
		assert.NotEmpty(t, got, "expected members before termination")
	})

	t.Run("organizations paginate, map, and skip missing id", func(t *testing.T) {
		t.Parallel()
		pager := mocks.NewMockOrgPager(gomock.NewController(t))
		gomock.InOrder(
			pager.EXPECT().ListOrgPage(gomock.Any(), gomock.Any()).Return([]auth0.RawOrg{
				{
					ID:          strptr("org_a"),
					Name:        strptr("acme"),
					DisplayName: strptr("Acme Corp"),
				},
				{ID: nil, Name: strptr("ghost")},
			}, strptr("1"), nil),
			pager.EXPECT().ListOrgPage(gomock.Any(), gomock.Any()).Return([]auth0.RawOrg{
				{ID: strptr("org_b"), Name: strptr("beta")},
			}, nil, nil),
		)

		got, err := testClient(t, pager, nil).ListOrganizations(ctx)

		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "org_a", got[0].ExtID)
		assert.Equal(t, "Acme Corp", got[0].Name)
		assert.Equal(t, "acme", got[0].Slug)
		assert.Equal(t, "org_b", got[1].ExtID)
		assert.Equal(t, "beta", got[1].Name, "display name falls back to machine name")
		assert.Equal(t, "beta", got[1].Slug)
	})
}

// TestAuth0Client_Writes tests the Auth0 write surface through the exported writer
// seam: create returns the id + forwards input, empty id is an error, update/delete
// forward args, and a writer error propagates.
//
// Why this test is important:
//   - This client is the single Auth0 writer for the identity service; a mapping or
//     error-handling regression silently corrupts org create/update/delete.
//
// What it tests:
//   - Create returns the id + forwards the write input; empty id → error; writer
//     error → error; Update forwards ext id; Delete forwards ext id + propagates error.
func TestAuth0Client_Writes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("create returns id and forwards input", func(t *testing.T) {
		t.Parallel()
		w := mocks.NewMockOrgWriter(gomock.NewController(t))
		w.EXPECT().
			CreateOrg(gomock.Any(), types.OrgWrite{Slug: "acme", Name: "Acme", LogoURL: "http://l"}).
			Return("org_new", nil)

		id, err := testWriteClient(t, w).
			CreateOrganization(ctx, types.OrgWrite{Slug: "acme", Name: "Acme", LogoURL: "http://l"})

		require.NoError(t, err)
		assert.Equal(t, "org_new", id)
	})

	t.Run("create errors when auth0 returns no id", func(t *testing.T) {
		t.Parallel()
		w := mocks.NewMockOrgWriter(gomock.NewController(t))
		w.EXPECT().CreateOrg(gomock.Any(), gomock.Any()).Return("", nil)

		_, err := testWriteClient(
			t,
			w,
		).CreateOrganization(ctx, types.OrgWrite{Slug: "acme"})

		require.Error(t, err, "empty org id from auth0 must be an error")
	})

	t.Run("create propagates writer error", func(t *testing.T) {
		t.Parallel()
		w := mocks.NewMockOrgWriter(gomock.NewController(t))
		w.EXPECT().CreateOrg(gomock.Any(), gomock.Any()).Return("", errors.New("boom"))

		_, err := testWriteClient(
			t,
			w,
		).CreateOrganization(ctx, types.OrgWrite{Slug: "acme"})

		require.Error(t, err)
	})

	t.Run("update forwards ext id", func(t *testing.T) {
		t.Parallel()
		w := mocks.NewMockOrgWriter(gomock.NewController(t))
		w.EXPECT().
			UpdateOrg(gomock.Any(), types.OrgWrite{ExtID: "org_1", Slug: "acme"}).
			Return(nil)

		require.NoError(t, testWriteClient(t, w).
			UpdateOrganization(ctx, types.OrgWrite{ExtID: "org_1", Slug: "acme"}))
	})

	t.Run("delete forwards ext id and propagates error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		w := mocks.NewMockOrgWriter(ctrl)
		w.EXPECT().DeleteOrg(gomock.Any(), "org_del").Return(nil)
		require.NoError(t, testWriteClient(t, w).DeleteOrganization(ctx, "org_del"))

		wErr := mocks.NewMockOrgWriter(ctrl)
		wErr.EXPECT().DeleteOrg(gomock.Any(), "org_del").Return(errors.New("nope"))
		require.Error(t, testWriteClient(t, wErr).DeleteOrganization(ctx, "org_del"))
	})
}
