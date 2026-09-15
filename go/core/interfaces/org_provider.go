package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
)

// OrgProvider reads organizations and their members, writes
// (creates/updates/deletes) organizations, and manages org invitations and member
// removal against an external auth provider (Auth0 today). It is the contract of
// the clients/auth tier; the Auth0 client and the in-memory stub are its
// interchangeable backends, selected by config.
type OrgProvider interface {
	// CreateOrganization creates an organization and returns its provider id.
	CreateOrganization(ctx context.Context, org types.OrgWrite) (string, error)
	// UpdateOrganization updates an existing organization (identified by org.ExtID).
	UpdateOrganization(ctx context.Context, org types.OrgWrite) error
	// DeleteOrganization permanently deletes an organization by its provider id.
	DeleteOrganization(ctx context.Context, orgExtID string) error
	// ListOrganizations returns every organization in the tenant.
	ListOrganizations(ctx context.Context) ([]types.Organization, error)
	// ListOrganizationMembers returns an organization's members with their roles.
	ListOrganizationMembers(ctx context.Context, orgExtID string) ([]types.Member, error)
	// InviteMembers sends org invitations, returning a per-email result.
	InviteMembers(
		ctx context.Context,
		orgExtID string,
		invites []types.InviteInput,
	) map[string]error
	// ListInvitations returns an organization's outstanding invitations.
	ListInvitations(ctx context.Context, orgExtID string) ([]types.Invitation, error)
	// DeleteInvitation revokes one outstanding invitation by its provider id
	// (types.Invitation.ID), invalidating the accept link it was emailed with.
	DeleteInvitation(ctx context.Context, orgExtID, invitationID string) error
	// RemoveOrganizationMember removes a user from an organization.
	RemoveOrganizationMember(ctx context.Context, orgExtID, userExtID string) error
}
