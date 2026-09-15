package unit_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/auth/auth0"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
)

// auth0Fixture is a canned Auth0 Management API served over an httptest TLS server.
// It drives the concrete go-auth0 SDK adapters (sdkOrgPager / sdkMemberPager /
// sdkMemberRoleLister / sdkOrgWriter / sdkInviter) through the exported auth0.New +
// Client methods — the only black-box surface that reaches those unexported adapters.
// Org id "org_err" makes every path-addressed endpoint 400 (member/roles/invitation/
// update/delete surfaces), "org_rolerr" lets members succeed but 400s the roles fan-out,
// and failList/failCreate toggle the two id-less endpoints (list/create organizations).
type auth0Fixture struct {
	srv        *httptest.Server
	host       string
	failList   atomic.Bool
	failCreate atomic.Bool
}

// writeJSON writes a JSON body with the given status.
func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// badRequest emits a 400 the go-auth0 error decoder maps to a BadRequestError.
func badRequest(w http.ResponseWriter) {
	writeJSON(
		w,
		http.StatusBadRequest,
		`{"statusCode":400,"error":"Bad Request","message":"bad","errorCode":"invalid_body"}`,
	)
}

// newAuth0Fixture starts the TLS server, points http.DefaultTransport at its
// cert-trusting transport for the duration of the test (the SDK's management HTTP
// client bottoms out at http.DefaultTransport, which auth0.New does not let a caller
// inject), and restores it on cleanup.
func newAuth0Fixture(t *testing.T) *auth0Fixture {
	t.Helper()
	f := &auth0Fixture{}
	mux := http.NewServeMux()

	// OAuth2 client-credentials token endpoint (fetched lazily on the first API call).
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK,
			`{"access_token":"test-token","token_type":"Bearer","expires_in":86400}`)
	})

	mux.HandleFunc(
		"GET /api/v2/organizations",
		func(w http.ResponseWriter, _ *http.Request) {
			if f.failList.Load() {
				badRequest(w)
				return
			}
			// org_a maps; the null element exercises the adapter's nil-skip; the id-less
			// element exercises ListOrganizations' missing-id skip.
			writeJSON(w, http.StatusOK, `{"organizations":[`+
				`{"id":"org_a","name":"acme","display_name":"Acme Corp"},`+
				`null,`+
				`{"name":"ghost"}`+
				`]}`)
		},
	)

	mux.HandleFunc(
		"POST /api/v2/organizations",
		func(w http.ResponseWriter, _ *http.Request) {
			if f.failCreate.Load() {
				badRequest(w)
				return
			}
			writeJSON(w, http.StatusOK, `{"id":"org_new"}`)
		},
	)

	mux.HandleFunc(
		"PATCH /api/v2/organizations/{id}",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") == "org_err" {
				badRequest(w)
				return
			}
			writeJSON(w, http.StatusOK, `{}`)
		},
	)

	mux.HandleFunc(
		"DELETE /api/v2/organizations/{id}",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") == "org_err" {
				badRequest(w)
				return
			}
			writeJSON(w, http.StatusOK, `{}`)
		},
	)

	mux.HandleFunc(
		"GET /api/v2/organizations/{id}/members",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") == "org_err" {
				badRequest(w)
				return
			}
			// u1 maps; null exercises the adapter nil-skip; the email-less member is dropped
			// by ListOrganizationMembers.
			writeJSON(w, http.StatusOK, `{"members":[`+
				`{"user_id":"auth0|u1","email":"u1@corp.test","name":"U1"},`+
				`null,`+
				`{"user_id":"auth0|noemail"}`+
				`]}`)
		},
	)

	mux.HandleFunc(
		"DELETE /api/v2/organizations/{id}/members",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") == "org_err" {
				badRequest(w)
				return
			}
			writeJSON(w, http.StatusOK, `{}`)
		},
	)

	mux.HandleFunc(
		"GET /api/v2/organizations/{id}/members/{uid}/roles",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") == "org_rolerr" {
				badRequest(w)
				return
			}
			// org-admin maps; null + the name-less role exercise the adapter's skips.
			writeJSON(w, http.StatusOK, `{"roles":[`+
				`{"id":"r1","name":"org-admin"},`+
				`null,`+
				`{"id":"r2"}`+
				`]}`)
		},
	)

	mux.HandleFunc(
		"POST /api/v2/organizations/{id}/invitations",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") == "org_err" {
				badRequest(w)
				return
			}
			writeJSON(w, http.StatusCreated, `{}`)
		},
	)

	mux.HandleFunc(
		"GET /api/v2/organizations/{id}/invitations",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") == "org_err" {
				badRequest(w)
				return
			}
			// First offset page carries data (with a null + an invitee-less entry to exercise
			// the loop's skips); any later page is empty so the offset pager terminates.
			if page := r.URL.Query().Get("page"); page != "" && page != "0" {
				writeJSON(w, http.StatusOK, `{"start":1,"limit":100,"invitations":[]}`)
				return
			}
			writeJSON(w, http.StatusOK, `{"start":0,"limit":100,"invitations":[`+
				`{"id":"uinv_1","invitee":{"email":"invitee@corp.test"},"roles":["role_1"],`+
				`"created_at":"2024-01-01T00:00:00Z","expires_at":"2024-01-08T00:00:00Z"},`+
				`null,`+
				`{"roles":["orphan"]}`+
				`]}`)
		},
	)

	mux.HandleFunc(
		"DELETE /api/v2/organizations/{id}/invitations/{invitationId}",
		func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") == "org_err" {
				badRequest(w)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		},
	)

	f.srv = httptest.NewTLSServer(mux)
	f.host = strings.TrimPrefix(f.srv.URL, "https://")

	origTransport := http.DefaultTransport
	http.DefaultTransport = f.srv.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = origTransport
		f.srv.Close()
	})
	return f
}

// client builds a fresh production Client (fresh resilience stack + breaker) against
// the fixture, so an error subtest never trips a breaker a later subtest depends on.
func (f *auth0Fixture) client(t *testing.T) *auth0.Client {
	t.Helper()
	c, err := auth0.New(context.Background(), auth0.Params{
		Domain:           f.host,
		ClientID:         "cid",
		ClientSecret:     "secret",
		PlatformClientID: "platform-app",
	})
	require.NoError(t, err)
	return c
}

// TestAuth0SDKAdapters_HTTP drives the concrete go-auth0 SDK adapters over an
// httptest server returning canned Management API JSON.
//
// Why this test is important:
//   - The sdk* adapters are the real wire between this client and Auth0; the seam
//     mocks (auth0_seams_test.go) prove the pagination/fan-out logic but never touch
//     the SDK request/response marshalling, so a broken path, verb, or JSON mapping
//     ships silently. This is the only black-box surface that exercises that wire.
//
// What it tests:
//   - Reads: organizations + members paginate and map, nil/id-less/email-less records
//     are skipped, and each member carries the roles fetched from the roles endpoint.
//   - Writes: create returns the new id, update/delete/remove/invite/revoke-invitation
//     reach the right verb+path, and invitations iterate the offset pager to termination.
//   - Errors: a 4xx on every endpoint surfaces as an error from the matching method.
func TestAuth0SDKAdapters_HTTP(t *testing.T) {
	f := newAuth0Fixture(t)
	ctx := context.Background()

	t.Run("list organizations maps and skips", func(t *testing.T) {
		orgs, err := f.client(t).ListOrganizations(ctx)
		require.NoError(t, err)
		require.Len(t, orgs, 1)
		assert.Equal(t, "org_a", orgs[0].ExtID)
		assert.Equal(t, "Acme Corp", orgs[0].Name)
		assert.Equal(t, "acme", orgs[0].Slug)
	})

	t.Run("list members maps, skips, and carries roles", func(t *testing.T) {
		members, err := f.client(t).ListOrganizationMembers(ctx, "org_ok")
		require.NoError(t, err)
		require.Len(t, members, 1)
		assert.Equal(t, "auth0|u1", members[0].Sub)
		assert.Equal(t, "u1@corp.test", members[0].Email)
		assert.Equal(t, []string{"org-admin"}, members[0].Roles)
	})

	t.Run("create organization returns new id", func(t *testing.T) {
		id, err := f.client(t).CreateOrganization(ctx,
			types.OrgWrite{
				Slug:    "acme",
				Name:    "Acme",
				LogoURL: "https://logo.example/l.png",
			})
		require.NoError(t, err)
		assert.Equal(t, "org_new", id)
	})

	t.Run("update organization reaches the patch endpoint", func(t *testing.T) {
		require.NoError(t, f.client(t).UpdateOrganization(ctx,
			types.OrgWrite{ExtID: "org_ok", Slug: "acme", Name: "Acme"}))
	})

	t.Run("delete organization reaches the delete endpoint", func(t *testing.T) {
		require.NoError(t, f.client(t).DeleteOrganization(ctx, "org_ok"))
	})

	t.Run("invite members reaches the invitation endpoint", func(t *testing.T) {
		ttl := 3600
		results := f.client(t).InviteMembers(ctx, "org_ok", []types.InviteInput{
			{
				Email:       "invitee@corp.test",
				InviterName: "Admin",
				RoleIDs:     []string{"role_1"},
				TTLSec:      &ttl,
			},
		})
		require.Len(t, results, 1)
		assert.NoError(t, results["invitee@corp.test"])
	})

	t.Run("list invitations iterates the offset pager", func(t *testing.T) {
		invites, err := f.client(t).ListInvitations(ctx, "org_ok")
		require.NoError(t, err)
		require.Len(t, invites, 1)
		assert.Equal(t, "invitee@corp.test", invites[0].Email)
		assert.Equal(t, []string{"role_1"}, invites[0].RoleIDs)
		// The invitation id is the handle DeleteInvitation revokes by, so it must survive
		// the SDK→domain mapping rather than being dropped with the rest of the record.
		assert.Equal(t, "uinv_1", invites[0].ID)
	})

	t.Run("delete invitation reaches the delete-invitation endpoint", func(t *testing.T) {
		require.NoError(t, f.client(t).DeleteInvitation(ctx, "org_ok", "uinv_1"))
	})

	t.Run("remove member reaches the delete-members endpoint", func(t *testing.T) {
		require.NoError(
			t,
			f.client(t).RemoveOrganizationMember(ctx, "org_ok", "auth0|u1"),
		)
	})

	t.Run("errors surface from every endpoint", func(t *testing.T) {
		f.failList.Store(true)
		_, err := f.client(t).ListOrganizations(ctx)
		f.failList.Store(false)
		require.Error(t, err, "list organizations")

		f.failCreate.Store(true)
		_, err = f.client(t).CreateOrganization(ctx, types.OrgWrite{Slug: "x"})
		f.failCreate.Store(false)
		require.Error(t, err, "create organization")

		_, err = f.client(t).ListOrganizationMembers(ctx, "org_err")
		require.Error(t, err, "list members")

		_, err = f.client(t).ListOrganizationMembers(ctx, "org_rolerr")
		require.Error(t, err, "list member roles")

		require.Error(
			t,
			f.client(t).UpdateOrganization(ctx, types.OrgWrite{ExtID: "org_err"}),
			"update organization",
		)
		require.Error(
			t,
			f.client(t).DeleteOrganization(ctx, "org_err"),
			"delete organization",
		)

		results := f.client(t).InviteMembers(ctx, "org_err",
			[]types.InviteInput{{Email: "x@corp.test"}})
		require.Error(t, results["x@corp.test"], "create invitation")

		_, err = f.client(t).ListInvitations(ctx, "org_err")
		require.Error(t, err, "list invitations")

		require.Error(t, f.client(t).DeleteInvitation(ctx, "org_err", "uinv_1"),
			"delete invitation")

		require.Error(t, f.client(t).RemoveOrganizationMember(ctx, "org_err", "auth0|u1"),
			"remove member")
	})
}

// TestAuth0New_InvalidDomain tests that New surfaces an error when the Auth0 domain
// cannot be parsed into a management-client base URL.
//
// Why this test is important:
//   - New is the production constructor; a malformed domain must fail at construction
//     with a wrapped error, not panic or yield a half-built client.
//
// What it tests:
//   - A domain containing an invalid URL escape makes New return a non-nil error.
func TestAuth0New_InvalidDomain(t *testing.T) {
	t.Parallel()
	_, err := auth0.New(context.Background(), auth0.Params{
		Domain:       "%zz",
		ClientID:     "cid",
		ClientSecret: "secret",
	})
	require.Error(t, err)
}
