// Package auth0 is the Auth0 Management API backend of the clients/auth tier: it
// reads the tenant's organizations and an organization's members (iterating every
// page, skipping malformed records) and writes organizations
// (create/update/delete), every call behind a circuit breaker. It satisfies
// interfaces.OrgProvider and is selected by auth.KindAuth0. Projections and inputs
// (core/types) are transport-neutral, so callers do not depend on the go-auth0 SDK
// types.
package auth0

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/auth0/go-auth0/v2/management"
	mgmclient "github.com/auth0/go-auth0/v2/management/client"
	auth0core "github.com/auth0/go-auth0/v2/management/core"
	"github.com/auth0/go-auth0/v2/management/option"

	clientstack "github.com/gt-tech-ai/knowledge-engine/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/lifecycle"
)

// The compile-time contract check lives in user.go: `var _ interfaces.UserProvider =
// (*Client)(nil)`. UserProvider embeds OrgProvider, so that single assertion proves both.

// RawOrg / RawMember are the nilable SDK projections; the read loops own the
// nil-skipping so it is unit-testable against a fake pager.
type RawOrg struct {
	// ID is the Auth0 organization id (nil when the SDK omitted it).
	ID *string
	// Name is the org's machine name / slug (nil when unset).
	Name *string
	// DisplayName is the human-readable org name (nil when unset).
	DisplayName *string
}

// RawMember is the nilable SDK projection of an organization member (see RawOrg).
type RawMember struct {
	// Sub is the member's Auth0 user id / subject (nil when unset).
	Sub *string
	// Email is the member's email address (nil when unset).
	Email *string
	// Name is the member's display name (nil when unset).
	Name *string
}

// OrgPager fetches one page of organizations; cursor is nil for the first page,
// and the returned next cursor is nil once no pages remain.
// SDK seam (charter §2.3): abstracts the go-auth0 SDK for the read/write loops.
type OrgPager interface {
	// ListOrgPage returns one page of organizations plus the next-page cursor
	// (nil once the last page is reached).
	ListOrgPage(
		ctx context.Context,
		cursor *string,
	) (orgs []RawOrg, next *string, err error)
}

// MemberPager fetches one page of an organization's members.
// SDK seam (charter §2.3): abstracts the go-auth0 SDK for the read/write loops.
type MemberPager interface {
	// ListMemberPage returns one page of the organization's members plus the
	// next-page cursor (nil once the last page is reached).
	ListMemberPage(
		ctx context.Context,
		orgExtID string,
		cursor *string,
	) (members []RawMember, next *string, err error)
}

// OrgWriter creates/updates/deletes Auth0 organizations. The SDK adapter
// implements it; a fake drives the write methods in tests.
// SDK seam (charter §2.3): abstracts the go-auth0 SDK for the read/write loops.
type OrgWriter interface {
	// CreateOrg creates an organization and returns its Auth0-assigned id.
	CreateOrg(ctx context.Context, org types.OrgWrite) (extID string, err error)
	// UpdateOrg updates the organization identified by org.ExtID in place.
	UpdateOrg(ctx context.Context, org types.OrgWrite) error
	// DeleteOrg deletes the organization identified by orgExtID.
	DeleteOrg(ctx context.Context, orgExtID string) error
}

// Inviter creates org invitations, lists an org's outstanding invitations, revokes
// one, and removes a member from an org. The SDK adapter implements it; a fake drives
// these in tests.
// SDK seam (charter §2.3): abstracts the go-auth0 SDK for the read/write loops.
type Inviter interface {
	// CreateInvitation creates one org invitation for the given invitee and roles.
	CreateInvitation(ctx context.Context, orgExtID string, inv types.InviteInput) error
	// ListInvitations returns the organization's outstanding invitations.
	ListInvitations(ctx context.Context, orgExtID string) ([]types.Invitation, error)
	// DeleteInvitation revokes one outstanding invitation by its provider invitation id.
	DeleteInvitation(ctx context.Context, orgExtID, invitationID string) error
	// RemoveMember removes a user from the organization (the user itself is not deleted).
	RemoveMember(ctx context.Context, orgExtID, userExtID string) error
}

// MemberRoleLister lists the Auth0 org role names assigned to one member. The SDK
// adapter implements it; a fake drives it in tests.
// SDK seam (charter §2.3): abstracts the go-auth0 SDK for the read/write loops.
type MemberRoleLister interface {
	// ListMemberRoles returns the names of the Auth0 org roles assigned to the member.
	ListMemberRoles(ctx context.Context, orgExtID, userSub string) ([]string, error)
}

// Client reads organizations and their members from the Auth0 Management API,
// writes (creates/updates/deletes) organizations, and manages org invitations and
// member removal.
type Client struct {
	// NoOp supplies the no-op Start/Stop: the Auth0 Management SDK client is stateless
	// (each call is an independent HTTPS request), so there is no connection to open or close.
	lifecycle.NoOp

	// orgs pages the tenant's organizations.
	orgs OrgPager
	// members pages an organization's members.
	members MemberPager
	// writer creates/updates/deletes organizations.
	writer OrgWriter
	// inviter creates/lists invitations and removes members.
	inviter Inviter
	// roleLister lists a member's Auth0 org role names.
	roleLister MemberRoleLister
	// userProvisioner creates users and assigns org roles (see user.go).
	userProvisioner UserProvisioner

	// stack runs every Management-API call through the shared client resilience
	// stack (Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics →
	// Logging), replacing the former hand-rolled circuit breaker.
	stack *clientstack.Stack

	// assignRetryMax / assignRetryBase bound the role-assignment retry that absorbs
	// Auth0's create→assign eventual-consistency lag: a just-created user can briefly
	// 404 (inexistent_user) on POST /users/{id}/roles before the tenant propagates it.
	// We retry the WRITE (not a GET /users/{id} poll) because the seed M2M client has
	// update:users but not read:users. Defaults set by the constructors; a test may
	// shrink them via WithAssignRetry. (Scalars last for optimal struct alignment.)
	assignRetryMax int
	// assignRetryBase is the base backoff between role-assignment retries (grows
	// per attempt; see assignRetryMax).
	assignRetryBase time.Duration
}

// Default bounds for the role-assignment propagation retry (see Client.assignRetryMax).
// Worst case ~200ms+400ms+800ms+1.6s+3.2s ≈ 6.2s, comfortably covering Auth0's typical
// sub-second-to-few-second create→assign propagation lag while staying within the op timeout.
const (
	// defaultAssignRetryMax is the default maximum number of role-assignment attempts.
	defaultAssignRetryMax = 5
	// defaultAssignRetryBase is the default base backoff between those attempts.
	defaultAssignRetryBase = 200 * time.Millisecond
)

// run executes a single Management-API call through the resilience stack, labelling
// it op. retryable enables the stack's Retry layer for idempotent reads/writes
// (off unless the stack is configured with a Retrier).
func (c *Client) run(
	ctx context.Context,
	op string,
	retryable bool,
	fn func(context.Context) error,
) error {
	_, err := clientstack.Run(ctx, c.stack, op, clientstack.RunOpts{Retryable: retryable},
		func(cctx context.Context) (struct{}, error) {
			return struct{}{}, fn(cctx)
		})
	return err
}

// Params holds the M2M credentials for the Auth0 Management API.
type Params struct {
	// Domain is the Auth0 tenant domain (e.g. "tenant.us.auth0.com").
	Domain string
	// ClientID is the M2M management API client id.
	ClientID string
	// ClientSecret is the M2M management API client secret (never logged).
	ClientSecret string

	// PlatformClientID is the Auth0 application client id that org invitations
	// grant access to (set as the invitation's ClientID). Only invitations use it;
	// empty is fine for callers that never invite.
	PlatformClientID string
}

// New builds a Client authenticated with the given M2M credentials.
func New(ctx context.Context, params Params) (*Client, error) {
	mgmt, err := mgmclient.New(
		params.Domain,
		option.WithClientCredentials(ctx, params.ClientID, params.ClientSecret),
	)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "build auth0 management client")
	}
	stack, err := clientstack.StackFromConfig(
		"auth0-read-client", clientstack.DefaultConfig(), clientstack.Deps{},
	)
	if err != nil {
		return nil, err
	}
	return &Client{
		orgs:    sdkOrgPager{client: mgmt},
		members: sdkMemberPager{client: mgmt},
		writer:  sdkOrgWriter{client: mgmt},
		inviter: sdkInviter{
			client:           mgmt,
			platformClientID: params.PlatformClientID,
		},
		roleLister:      sdkMemberRoleLister{client: mgmt},
		userProvisioner: sdkUserProvisioner{client: mgmt},
		assignRetryMax:  defaultAssignRetryMax,
		assignRetryBase: defaultAssignRetryBase,
		stack:           stack,
	}, nil
}

// NewWithSeams builds a Client over injected SDK-adapter seams and circuit breaker,
// so black-box tests can drive the pagination / nil-skip / role-fan-out logic
// against generated mocks without a live Auth0 tenant. Production uses New.
// opts inject additional seams (e.g. WithUserProvisioner) as variadic options, so the
// 6-arg call sites for the org/invite tests keep compiling unchanged.
func NewWithSeams(
	orgs OrgPager,
	members MemberPager,
	writer OrgWriter,
	inv Inviter,
	roleLister MemberRoleLister,
	cb interfaces.CircuitBreaker,
	opts ...SeamOption,
) *Client {
	c := &Client{
		orgs:            orgs,
		members:         members,
		writer:          writer,
		inviter:         inv,
		roleLister:      roleLister,
		assignRetryMax:  defaultAssignRetryMax,
		assignRetryBase: defaultAssignRetryBase,
		stack:           clientstack.New("auth0-read-client").WithCircuitBreaker(cb),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// CreateOrganization creates an organization in Auth0 and returns the new org's
// Auth0 id, running the call through the circuit breaker.
func (c *Client) CreateOrganization(
	ctx context.Context,
	org types.OrgWrite,
) (string, error) {
	var extID string
	err := c.run(ctx, "create_organization", false, func(cctx context.Context) error {
		var e error
		extID, e = c.writer.CreateOrg(cctx, org)
		return e
	})
	if err != nil {
		return "", mapErr(err, "create organization")
	}
	if extID == "" {
		return "", errors.Upstream("org created - auth0 did not respond with an org_id")
	}
	return extID, nil
}

// UpdateOrganization updates an existing Auth0 organization (identified by
// org.ExtID), running the call through the circuit breaker.
func (c *Client) UpdateOrganization(ctx context.Context, org types.OrgWrite) error {
	err := c.run(ctx, "update_organization", true,
		func(cctx context.Context) error { return c.writer.UpdateOrg(cctx, org) })
	if err != nil {
		return mapErr(err, "update organization")
	}
	return nil
}

// DeleteOrganization permanently deletes an Auth0 organization (removing its
// members from the org, but not the users themselves), running the call through
// the circuit breaker.
func (c *Client) DeleteOrganization(ctx context.Context, orgExtID string) error {
	err := c.run(ctx, "delete_organization", true,
		func(cctx context.Context) error { return c.writer.DeleteOrg(cctx, orgExtID) })
	if err != nil {
		return mapErr(err, "delete organization")
	}
	return nil
}

// ListOrganizations returns every organization in the tenant, iterating all pages
// and skipping any org missing an id. The slug is Auth0's machine name; the name
// falls back to the machine name when no display name is set.
func (c *Client) ListOrganizations(ctx context.Context) ([]types.Organization, error) {
	raws, err := paginate(ctx, c, "list_organizations",
		func(cctx context.Context, cursor *string) ([]RawOrg, *string, error) {
			return c.orgs.ListOrgPage(cctx, cursor)
		})
	if err != nil {
		return nil, mapErr(err, "list organizations")
	}
	out := make([]types.Organization, 0, len(raws))
	for _, o := range raws {
		if o.ID == nil || *o.ID == "" {
			continue
		}
		name := deref(o.DisplayName)
		if name == "" {
			name = deref(o.Name)
		}
		out = append(
			out,
			// Slugify the Auth0 org name: Auth0 permits underscores (and other chars) in an org name,
			// but our slug rule (common.v1.slug + the Ent Match backstop) is hyphen-only. Passing the
			// name verbatim would make a real org (e.g. "acme_corp") fail validation on seed and be
			// un-editable (a settings update round-trips the slug). Slugifying keeps a stable URL slug.
			types.Organization{ExtID: *o.ID, Name: name, Slug: slugify(deref(o.Name))},
		)
	}
	return out, nil
}

// slugify converts an arbitrary org name to a URL slug matching common.v1.slug
// (^[a-z0-9]+(?:-[a-z0-9]+)*$): lower-cased, every run of non-alphanumerics collapsed to a single
// hyphen, and leading/trailing hyphens trimmed. An empty/all-symbol name yields "" (the caller's
// converge-by-slug logic then falls back), rather than an invalid slug.
func slugify(name string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevHyphen = false
		case !prevHyphen && b.Len() > 0:
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// ListOrganizationMembers returns an organization's members, each with their Auth0
// org role names, iterating all pages and dropping any member missing a sub or
// email (they cannot be resolved or violate the unique-email constraint). An org
// with no usable members yields an empty slice — the caller decides whether that is
// an error. Each member's roles are one Auth0 call; those O(members) calls run as a
// bounded concurrent fan-out (cap 12, mirroring InviteMembers) so seeding a large
// tenant is not serialized, each still through the circuit breaker. On any role-fetch
// failure the whole read fails, returning the first (lowest-index) error — a
// deterministic selection, though the specific error's identity can vary with
// scheduling when the shared breaker trips mid-fan-out.
func (c *Client) ListOrganizationMembers(
	ctx context.Context,
	orgExtID string,
) ([]types.Member, error) {
	raws, err := paginate(ctx, c, "list_organization_members",
		func(cctx context.Context, cursor *string) ([]RawMember, *string, error) {
			return c.members.ListMemberPage(cctx, orgExtID, cursor)
		})
	if err != nil {
		return nil, mapErr(err, "list organization members")
	}
	// Filter to resolvable members BEFORE indexing, so the fan-out writes disjoint
	// out[i] slots in member order (no lock, no data race).
	out := make([]types.Member, 0, len(raws))
	for _, m := range raws {
		if m.Sub == nil || *m.Sub == "" || m.Email == nil || *m.Email == "" {
			continue
		}
		out = append(out, types.Member{Sub: *m.Sub, Email: *m.Email, Name: deref(m.Name)})
	}
	// Fetch every member's roles concurrently, bounded to 12 in flight (the acquire
	// before wg.Go applies backpressure), each through the circuit breaker. Roles land
	// in the member's own slot; errs[i] captures a per-member failure.
	var wg sync.WaitGroup
	sem := make(chan struct{}, 12) // limit to 12 concurrent Management API calls
	errs := make([]error, len(out))
	for i := range out {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			var roles []string
			if err := c.run(
				ctx,
				"list_member_roles",
				true,
				func(cctx context.Context) error {
					var e error
					roles, e = c.roleLister.ListMemberRoles(cctx, orgExtID, out[i].Sub)
					return e
				},
			); err != nil {
				errs[i] = err
				return
			}
			out[i].Roles = roles
		})
	}
	wg.Wait()
	// Fail the read on any role-fetch error, returning the lowest-index one so the
	// error is deterministic despite the concurrent fan-out.
	for _, e := range errs {
		if e != nil {
			return nil, mapErr(e, "list organization member roles")
		}
	}
	return out, nil
}

// InviteMembers sends the given org invitations concurrently, returning a
// per-email result (nil on success, a mapped error otherwise). Each invitation
// runs through the circuit breaker. RoleIDs on each input must be resolved Auth0
// role ids; the granted application is the client's PlatformClientID.
func (c *Client) InviteMembers(
	ctx context.Context,
	orgExtID string,
	invites []types.InviteInput,
) map[string]error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 12) // limit to 12 concurent invite requests
	results := make(map[string]error, len(invites))
	for _, inv := range invites {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			err := c.run(
				ctx,
				"create_invitation",
				false,
				func(cctx context.Context) error {
					return c.inviter.CreateInvitation(cctx, orgExtID, inv)
				},
			)
			if err != nil {
				err = mapErr(err, fmt.Sprintf("create invitation for %s", inv.Email))
			}
			mu.Lock()
			results[inv.Email] = err
			mu.Unlock()
		})
	}
	wg.Wait()
	return results
}

// ListInvitations returns an organization's outstanding (pending or expired)
// invitations, iterating every page, run through the circuit breaker.
func (c *Client) ListInvitations(
	ctx context.Context,
	orgExtID string,
) ([]types.Invitation, error) {
	var out []types.Invitation
	err := c.run(ctx, "list_invitations", true, func(cctx context.Context) error {
		var e error
		out, e = c.inviter.ListInvitations(cctx, orgExtID)
		return e
	})
	if err != nil {
		return nil, mapErr(err, "list organization invitations")
	}
	return out, nil
}

// DeleteInvitation revokes one outstanding org invitation by its Auth0 invitation id,
// run through the circuit breaker. Revoking invalidates the accept link the invitee was
// emailed, so it is the first half of a resend (revoke, then create a fresh invitation)
// as well as a cancel on its own.
func (c *Client) DeleteInvitation(
	ctx context.Context,
	orgExtID, invitationID string,
) error {
	err := c.run(
		ctx,
		"delete_invitation",
		true,
		func(cctx context.Context) error {
			return c.inviter.DeleteInvitation(cctx, orgExtID, invitationID)
		},
	)
	if err != nil {
		return mapErr(
			err,
			fmt.Sprintf(
				"delete invitation %s of organization %s",
				invitationID,
				orgExtID,
			),
		)
	}
	return nil
}

// RemoveOrganizationMember removes a user from an organization (the user itself is
// not deleted), run through the circuit breaker.
func (c *Client) RemoveOrganizationMember(
	ctx context.Context,
	orgExtID, userExtID string,
) error {
	err := c.run(
		ctx,
		"remove_organization_member",
		true,
		func(cctx context.Context) error {
			return c.inviter.RemoveMember(cctx, orgExtID, userExtID)
		},
	)
	if err != nil {
		return mapErr(
			err,
			fmt.Sprintf("remove user %s from organization %s", userExtID, orgExtID),
		)
	}
	return nil
}

// paginate accumulates every page returned by fetch, running each page fetch
// through the client resilience stack (op labels the read). It stops when the next
// cursor is nil, and defends against a misbehaving endpoint whose cursor never
// advances (stop instead of spin).
func paginate[T any](
	ctx context.Context,
	c *Client,
	op string,
	fetch func(ctx context.Context, cursor *string) ([]T, *string, error),
) ([]T, error) {
	var (
		out    []T
		cursor *string
		seen   = map[string]bool{}
	)
	for {
		var (
			page []T
			next *string
		)
		if err := c.run(ctx, op, true, func(cctx context.Context) error {
			var e error
			page, next, e = fetch(cctx, cursor)
			return e
		}); err != nil {
			return nil, err
		}
		out = append(out, page...)
		if next == nil {
			break
		}
		// Defend against a misbehaving endpoint whose cursor never terminates —
		// whether it freezes on one value or cycles between several. Stop the first
		// time a cursor value repeats instead of spinning until OOM.
		if seen[*next] {
			break
		}
		seen[*next] = true
		cursor = next
	}
	return out, nil
}

// sdkOrgPager adapts the go-auth0 client to OrgPager.
type sdkOrgPager struct {
	// client is the go-auth0 management SDK client.
	client *mgmclient.Management
}

// ListOrgPage fetches one page of organizations, returning the page's orgs and the
// next-page cursor (nil once the last page is reached).
func (s sdkOrgPager) ListOrgPage(
	ctx context.Context,
	cursor *string,
) ([]RawOrg, *string, error) {
	// From: nil (the first-page cursor) is equivalent to omitting From, so a single
	// call covers both the first and subsequent pages.
	page, err := s.client.Organizations.List(
		ctx,
		&management.ListOrganizationsRequestParameters{From: cursor},
	)
	if err != nil {
		return nil, nil, err
	}
	orgs := make([]RawOrg, 0, len(page.Results))
	for _, o := range page.Results {
		if o == nil {
			continue
		}
		orgs = append(orgs, RawOrg{ID: o.ID, Name: o.Name, DisplayName: o.DisplayName})
	}
	return orgs, page.RawResponse.Next, nil
}

// sdkMemberPager adapts the go-auth0 client to MemberPager.
type sdkMemberPager struct {
	// client is the go-auth0 management SDK client.
	client *mgmclient.Management
}

// ListMemberPage fetches one page of an organization's members, returning the page's
// members and the next-page cursor (nil once the last page is reached).
func (s sdkMemberPager) ListMemberPage(
	ctx context.Context,
	orgExtID string,
	cursor *string,
) ([]RawMember, *string, error) {
	page, err := s.client.Organizations.Members.List(
		ctx,
		orgExtID,
		&management.ListOrganizationMembersRequestParameters{From: cursor},
	)
	if err != nil {
		return nil, nil, err
	}
	members := make([]RawMember, 0, len(page.Results))
	for _, m := range page.Results {
		if m == nil {
			continue
		}
		members = append(members, RawMember{Sub: m.UserID, Email: m.Email, Name: m.Name})
	}
	return members, page.RawResponse.Next, nil
}

// sdkMemberRoleLister adapts the go-auth0 client to MemberRoleLister.
type sdkMemberRoleLister struct {
	// client is the go-auth0 management SDK client.
	client *mgmclient.Management
}

// ListMemberRoles returns the names of the Auth0 org roles assigned to the member.
// A member has only a handful of org roles, so the first page covers them.
func (s sdkMemberRoleLister) ListMemberRoles(
	ctx context.Context,
	orgExtID, userSub string,
) ([]string, error) {
	page, err := s.client.Organizations.Members.Roles.List(
		ctx,
		orgExtID,
		userSub,
		&management.ListOrganizationMemberRolesRequestParameters{},
	)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(page.Results))
	for _, r := range page.Results {
		if r == nil || r.Name == nil {
			continue
		}
		names = append(names, *r.Name)
	}
	return names, nil
}

// sdkOrgWriter adapts the go-auth0 client to OrgWriter.
type sdkOrgWriter struct {
	// client is the go-auth0 management SDK client.
	client *mgmclient.Management
}

// CreateOrg creates the organization and returns its Auth0-assigned id.
func (s sdkOrgWriter) CreateOrg(ctx context.Context, org types.OrgWrite) (string, error) {
	resp, err := s.client.Organizations.Create(
		ctx,
		&management.CreateOrganizationRequestContent{
			Name:        org.Slug,
			DisplayName: &org.Name,
			Branding:    branding(org.LogoURL),
		},
	)
	if err != nil {
		return "", err
	}
	return deref(resp.ID), nil
}

// UpdateOrg updates the organization identified by org.ExtID in place.
func (s sdkOrgWriter) UpdateOrg(ctx context.Context, org types.OrgWrite) error {
	_, err := s.client.Organizations.Update(
		ctx,
		org.ExtID,
		&management.UpdateOrganizationRequestContent{
			Name:        &org.Slug,
			DisplayName: &org.Name,
			Branding:    branding(org.LogoURL),
		},
	)
	return err
}

// DeleteOrg deletes the organization identified by orgExtID.
func (s sdkOrgWriter) DeleteOrg(ctx context.Context, orgExtID string) error {
	return s.client.Organizations.Delete(ctx, orgExtID)
}

// sdkInviter adapts the go-auth0 client to inviter.
type sdkInviter struct {
	// client is the go-auth0 management SDK client.
	client *mgmclient.Management

	// platformClientID is the Auth0 application the invitation grants access to.
	platformClientID string
}

// CreateInvitation creates one org invitation for the given invitee and roles.
func (s sdkInviter) CreateInvitation(
	ctx context.Context,
	orgExtID string,
	inv types.InviteInput,
) error {
	_, err := s.client.Organizations.Invitations.Create(
		ctx,
		orgExtID,
		&management.CreateOrganizationInvitationRequestContent{
			Inviter:  &management.OrganizationInvitationInviter{Name: inv.InviterName},
			Invitee:  &management.OrganizationInvitationInvitee{Email: inv.Email},
			ClientID: s.platformClientID,
			TTLSec:   inv.TTLSec,
			Roles:    inv.RoleIDs,
		},
	)
	return err
}

// ListInvitations returns every outstanding invitation for the organization,
// iterating all pages via the SDK pager (ErrNoPages terminates the walk).
func (s sdkInviter) ListInvitations(
	ctx context.Context,
	orgExtID string,
) ([]types.Invitation, error) {
	perPage := 100
	page, err := s.client.Organizations.Invitations.List(
		ctx,
		orgExtID,
		&management.ListOrganizationInvitationsRequestParameters{PerPage: &perPage},
	)
	if err != nil {
		return nil, err
	}
	var out []types.Invitation
	for page != nil {
		for _, in := range page.Results {
			if in == nil || in.Invitee == nil {
				continue
			}
			out = append(out, types.Invitation{
				ID:        in.GetID(),
				Email:     in.Invitee.Email,
				RoleIDs:   in.Roles,
				ExpiresAt: in.GetExpiresAt(),
				CreatedAt: in.GetCreatedAt(),
			})
		}
		next, err := page.GetNextPage(ctx)
		if err != nil {
			if errors.StdIs(err, auth0core.ErrNoPages) {
				break
			}
			return nil, err
		}
		page = next
	}
	return out, nil
}

// DeleteInvitation revokes one outstanding org invitation by its Auth0 invitation id.
func (s sdkInviter) DeleteInvitation(
	ctx context.Context,
	orgExtID, invitationID string,
) error {
	return s.client.Organizations.Invitations.Delete(ctx, orgExtID, invitationID)
}

// RemoveMember removes a single user from an organization.
func (s sdkInviter) RemoveMember(ctx context.Context, orgExtID, userExtID string) error {
	return s.client.Organizations.Members.Delete(
		ctx,
		orgExtID,
		&management.DeleteOrganizationMembersRequestContent{Members: []string{userExtID}},
	)
}

// branding builds the SDK organization branding payload, leaving it unset (nil)
// when no logo URL is provided.
func branding(logoURL string) *management.OrganizationBranding {
	if logoURL == "" {
		return nil
	}
	return &management.OrganizationBranding{LogoURL: &logoURL}
}

// isConflictErr reports whether err is an Auth0 409 ConflictError (used by CreateUser to
// treat "user already exists" as an idempotent re-run rather than a fatal upstream error).
func isConflictErr(err error) bool {
	var conf *management.ConflictError
	return errors.As(err, &conf)
}

// isNotFoundErr reports whether err is an Auth0 404 (NotFoundError). A role assignment
// against a just-created user can transiently 404 (inexistent_user) until the tenant
// propagates the new user — the retriable case CreateUser absorbs (see user.go).
func isNotFoundErr(err error) bool {
	var nf *management.NotFoundError
	return errors.As(err, &nf)
}

// mapErr maps an Auth0 SDK error to a domain error (the canonical mapping for both
// the read and write surfaces).
func mapErr(err error, msg string) error {
	var br *management.BadRequestError
	if errors.As(err, &br) {
		return errors.Wrap(err, errors.CodeInvalidInput, msg)
	}
	var nf *management.NotFoundError
	if errors.As(err, &nf) {
		return errors.Wrap(err, errors.CodeNotFound, msg)
	}
	var conf *management.ConflictError
	if errors.As(err, &conf) {
		return errors.Wrap(err, errors.CodeConflict, msg)
	}
	var auth *management.UnauthorizedError
	var creds *management.ForbiddenError
	if errors.As(err, &auth) || errors.As(err, &creds) {
		return errors.Wrap(err, errors.CodeInternal, msg)
	}
	return errors.Wrap(err, errors.CodeUpstream, msg)
}

// deref returns the pointed-to string, or "" when nil.
func deref(p *string) string {
	if p != nil {
		return *p
	}
	return ""
}
