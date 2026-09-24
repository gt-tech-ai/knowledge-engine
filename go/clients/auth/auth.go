// Package auth is the auth client tier: it selects an organization/identity
// provider backend — Auth0 today (auth0/), an in-memory stub (stub/) for dev/test —
// by Kind and returns the interfaces.UserProvider contract (OrgProvider plus user
// provisioning), so switching auth providers is a config change, not a caller edit. The
// contract lives in core (interfaces.UserProvider + core/types); each backend implements
// it; this is the factory, which also wraps the provider in the fail-closed prod-guard
// decorator (CreateUser refused in production).
package auth

import (
	"context"
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/auth/auth0"
	"github.com/gt-tech-ai/knowledge-engine/go/clients/auth/stub"
	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// Kind selects the auth provider backend.
type Kind int

const (
	// KindAuth0 uses the Auth0 Management API (production).
	KindAuth0 Kind = iota
	// KindStub uses an in-memory provider (dev/test) — no external calls.
	KindStub
)

// String returns the string form of Kind.
func (k Kind) String() string {
	switch k {
	case KindAuth0:
		return "auth0"
	case KindStub:
		return "stub"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Auth0Config holds the Auth0 Management API settings for the auth0 backend, so
// callers configure the provider through the auth tier without touching the SDK.
type Auth0Config struct {
	// Domain is the Auth0 tenant domain (e.g. "tenant.us.auth0.com").
	Domain string
	// ClientID is the M2M management API client id.
	ClientID string
	// ClientSecret is the M2M management API client secret (never logged).
	ClientSecret string
	// PlatformClientID is the Auth0 application id that org invitations grant access to.
	PlatformClientID string
}

// Config selects and configures the auth provider backend.
type Config struct {
	// Auth0 holds the Auth0 Management API settings (used when Kind==KindAuth0).
	Auth0 Auth0Config

	// Env is the resolved deploy environment (SEARCH_ENV). It drives the fail-closed
	// prod-guard decorator around CreateUser: only an allow-listed non-prod env (""/dev/
	// staging/test/local) permits user provisioning; any other value is refused.
	Env string

	// Kind selects the backend. The zero value is KindAuth0.
	Kind Kind
}

// NewFromConfig builds the auth provider selected by cfg.Kind: the Auth0 Management
// API client (KindAuth0) or the in-memory stub (KindStub). It is the app-wiring
// entrypoint mirroring the cache/storage NewFromConfig factory. Both backends satisfy
// interfaces.UserProvider (OrgProvider + user provisioning).
func NewFromConfig(ctx context.Context, cfg Config) (interfaces.UserProvider, error) {
	var (
		provider interfaces.UserProvider
		err      error
	)
	switch cfg.Kind {
	case KindStub:
		provider = stub.New()
	case KindAuth0:
		// Validate the M2M creds at wiring time (the Management token is fetched lazily, so
		// otherwise a blank secret constructs cleanly and only fails deep in the first call).
		if cfg.Auth0.Domain == "" || cfg.Auth0.ClientID == "" ||
			cfg.Auth0.ClientSecret == "" {
			return nil, errors.New(
				errors.CodeInvalidInput,
				"auth0 provider requires a non-empty domain, client_id, and client_secret",
			)
		}
		provider, err = auth0.New(ctx, auth0.Params{
			Domain:           cfg.Auth0.Domain,
			ClientID:         cfg.Auth0.ClientID,
			ClientSecret:     cfg.Auth0.ClientSecret,
			PlatformClientID: cfg.Auth0.PlatformClientID,
		})
	default:
		return nil, errors.New(
			errors.CodeInvalidInput,
			fmt.Sprintf("unknown auth kind: %v", cfg.Kind),
		)
	}
	if err != nil {
		return nil, err
	}
	// Wrap in the fail-closed prod guard (a decorator, ARCHITECTURE.md#decorators) so CreateUser
	// is refused in prod.
	return newProdGuard(provider, cfg.Env), nil
}
