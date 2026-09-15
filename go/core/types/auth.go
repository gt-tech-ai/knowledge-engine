package types

import "time"

// Organization is a read projection of an organization in an external auth provider
// (e.g. Auth0 Organizations).
type Organization struct {
	// ExtID is the provider's organization id (e.g. Auth0 org_…) — the natural key downstream.
	ExtID string
	// Name is the friendly display name (falls back to the machine name).
	Name string
	// Slug is the machine name (already slug-valid).
	Slug string
}

// Member is a read projection of an organization member with a resolved
// (non-empty) sub and email.
type Member struct {
	// Sub is the provider subject id (the identity lookup key).
	Sub string
	// Email is the member's email (guaranteed non-empty).
	Email string
	// Name is the member's display name (may be empty).
	Name string
	// Roles are the provider organization role NAMES assigned to the member (e.g.
	// "org-admin"). Callers map these to their own role model. Empty when the member
	// has no org-level roles.
	Roles []string
}

// OrgWrite is the transport-neutral input for creating or updating an
// organization, so callers do not depend on a provider SDK's request types.
type OrgWrite struct {
	// ExtID is the provider organization id. Required for Update; ignored for Create
	// (the provider assigns the id).
	ExtID string

	// Slug is the provider machine name (the organization's "name" field).
	Slug string

	// Name is the friendly display name.
	Name string

	// LogoURL is the organization branding logo URL; empty leaves it unset.
	LogoURL string
}

// InviteInput is the transport-neutral input for one org invitation. RoleIDs are
// provider role ids the caller has already resolved (the mapping from an app's own
// role model to provider role ids lives with the caller); TTLSec is the invitation
// lifetime in seconds (nil = the provider default).
type InviteInput struct {
	// TTLSec is the invitation lifetime in seconds (nil = the provider default).
	TTLSec *int
	// Email is the invitee's email address.
	Email string
	// InviterName is the display name shown as the sender of the invitation.
	InviterName string
	// RoleIDs are the provider role ids to grant the invitee (already resolved by the caller).
	RoleIDs []string
}

// Invitation is a read projection of an outstanding org invitation. RoleIDs are the
// raw provider role ids granted; the caller maps them back to its own model.
type Invitation struct {
	// ExpiresAt is when the invitation lapses.
	ExpiresAt time.Time
	// CreatedAt is when the invitation was issued.
	CreatedAt time.Time
	// ID is the provider's own identifier for this invitation — the handle a caller
	// passes back to revoke it. Opaque and provider-shaped (an Auth0 invitation id is
	// not a UUID); empty when the backend does not identify invitations.
	ID string
	// Email is the invited address.
	Email string
	// RoleIDs are the raw provider role ids granted; the caller maps them back to its own model.
	RoleIDs []string
}

// UserCreate is the transport-neutral input for provisioning a login-capable user in an
// external auth provider (e.g. Auth0 Users). RoleIDs are provider role ids the caller has
// already resolved (org role + clearance level), mirroring InviteInput; the helper creates
// the user then assigns those roles.
type UserCreate struct {
	// Email is the new user's email address (the login identifier).
	Email string
	// Connection is the auth provider identity connection to create the user in
	// (e.g. an Auth0 database connection such as Username-Password-Authentication).
	Connection string
	// Password is the user's initial password.
	Password string
	// UserID is an optional caller-supplied provider user id; setting it deterministically
	// makes re-provisioning idempotent (a re-run reuses the same user rather than duplicating).
	UserID string
	// RoleIDs are the provider role ids to assign after creation (already resolved by the caller).
	RoleIDs []string
	// EmailVerified marks the user's email as already verified at creation (so synthetic
	// personas receive no verification mail).
	EmailVerified bool
}
