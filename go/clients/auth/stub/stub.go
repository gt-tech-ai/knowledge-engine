// Package stub is the in-memory backend of the clients/auth tier: it satisfies
// interfaces.UserProvider (OrgProvider + user provisioning) with process-local state, so a
// service (or test) can run the organization/member, invitation, and user-provisioning
// flows without a real auth provider. It is selected by auth.KindStub.
package stub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/lifecycle"
)

// stubInvitationTTL is the invitation lifetime the stub applies when an InviteInput
// carries no explicit TTL, standing in for the provider default.
const stubInvitationTTL = 7 * 24 * time.Hour

// Provider is the in-memory auth provider. All state is process-local and guarded
// by mu, so it is safe for concurrent use.
type Provider struct {
	// NoOp supplies the no-op Start/Stop: the stub holds only process-local in-memory state,
	// so there is no resource to open or close.
	lifecycle.NoOp

	// orgs holds each organization keyed by its external id.
	orgs map[string]types.Organization
	// members holds each organization's members keyed by org external id.
	members map[string][]types.Member
	// invites holds each organization's invitations keyed by org external id.
	invites map[string][]types.Invitation
	// nextID is the counter used to mint successive synthetic ids.
	nextID int
	// mu guards all of the maps and nextID for concurrent use.
	mu sync.Mutex
}

// compile-time check: the stub satisfies the interfaces.UserProvider contract
// (interfaces.OrgProvider plus CreateUser).
var _ interfaces.UserProvider = (*Provider)(nil)

// New returns an empty in-memory auth provider.
func New() *Provider {
	return &Provider{
		orgs:    map[string]types.Organization{},
		members: map[string][]types.Member{},
		invites: map[string][]types.Invitation{},
	}
}

// CreateOrganization stores a new organization under a synthetic id and returns it.
func (p *Provider) CreateOrganization(
	_ context.Context,
	org types.OrgWrite,
) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	id := fmt.Sprintf("org_stub_%d", p.nextID)
	p.orgs[id] = types.Organization{ExtID: id, Name: org.Name, Slug: org.Slug}
	return id, nil
}

// UpdateOrganization updates a stored organization identified by org.ExtID.
func (p *Provider) UpdateOrganization(_ context.Context, org types.OrgWrite) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.orgs[org.ExtID] = types.Organization{
		ExtID: org.ExtID,
		Name:  org.Name,
		Slug:  org.Slug,
	}
	return nil
}

// DeleteOrganization removes a stored organization and its members/invitations.
func (p *Provider) DeleteOrganization(_ context.Context, orgExtID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.orgs, orgExtID)
	delete(p.members, orgExtID)
	delete(p.invites, orgExtID)
	return nil
}

// ListOrganizations returns every stored organization.
func (p *Provider) ListOrganizations(_ context.Context) ([]types.Organization, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]types.Organization, 0, len(p.orgs))
	for _, o := range p.orgs {
		out = append(out, o)
	}
	return out, nil
}

// ListOrganizationMembers returns the stored members of an organization.
func (p *Provider) ListOrganizationMembers(
	_ context.Context, orgExtID string,
) ([]types.Member, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]types.Member(nil), p.members[orgExtID]...), nil
}

// InviteMembers records one outstanding invitation per input and reports success
// for each email. Each invitation is stamped with a synthetic id (the handle
// DeleteInvitation revokes it by) and a created/expires window derived from the
// input TTL, so the stub-backed listing renders the same shape Auth0 returns.
func (p *Provider) InviteMembers(
	_ context.Context, orgExtID string, invites []types.InviteInput,
) map[string]error {
	p.mu.Lock()
	defer p.mu.Unlock()
	results := make(map[string]error, len(invites))
	now := time.Now()
	for _, inv := range invites {
		ttl := stubInvitationTTL
		if inv.TTLSec != nil {
			ttl = time.Duration(*inv.TTLSec) * time.Second
		}
		p.nextID++
		p.invites[orgExtID] = append(p.invites[orgExtID], types.Invitation{
			ID:        fmt.Sprintf("invitation_stub_%d", p.nextID),
			Email:     inv.Email,
			RoleIDs:   inv.RoleIDs,
			CreatedAt: now,
			ExpiresAt: now.Add(ttl),
		})
		results[inv.Email] = nil
	}
	return results
}

// ListInvitations returns an organization's outstanding invitations.
func (p *Provider) ListInvitations(
	_ context.Context, orgExtID string,
) ([]types.Invitation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]types.Invitation(nil), p.invites[orgExtID]...), nil
}

// DeleteInvitation revokes the stored invitation with the given id. It is a no-op when
// no such invitation exists, mirroring the idempotent revoke a caller can safely retry.
func (p *Provider) DeleteInvitation(
	_ context.Context,
	orgExtID, invitationID string,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.invites[orgExtID][:0]
	for _, inv := range p.invites[orgExtID] {
		if inv.ID != invitationID {
			kept = append(kept, inv)
		}
	}
	p.invites[orgExtID] = kept
	return nil
}

// RemoveOrganizationMember removes a member (by sub) from an organization.
func (p *Provider) RemoveOrganizationMember(
	_ context.Context,
	orgExtID, userExtID string,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.members[orgExtID][:0]
	for _, m := range p.members[orgExtID] {
		if m.Sub != userExtID {
			kept = append(kept, m)
		}
	}
	p.members[orgExtID] = kept
	return nil
}

// CreateUser mints a synthetic, login-shaped user id so the no-creds seed path can run
// without a live Auth0 tenant. It performs no I/O and sends no verification email
// (spec.EmailVerified is honoured as a no-op); spec.RoleIDs are accepted but not stored,
// since nothing reads stub user roles yet.
func (p *Provider) CreateUser(_ context.Context, _ types.UserCreate) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	return fmt.Sprintf("auth0|user_stub_%d", p.nextID), nil
}
