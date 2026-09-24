package infra

import coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"

// Auth0Config holds Auth0 tenant connection parameters (all non-secret).
type Auth0Config struct {
	// Domain is the Auth0 tenant domain (e.g. "dev-xyz.us.auth0.com").
	Domain string `mapstructure:"domain" envalias:"SEARCH_AUTH0_DOMAIN,AUTH0_DOMAIN"`

	// ClientID is the Auth0 M2M application client ID.
	ClientID string `mapstructure:"client_id" envalias:"SEARCH_AUTH0_CLIENT_ID,AUTH0_CLIENT_ID"`

	// Audience is the API resource server audience identifier for JWT validation.
	Audience string `mapstructure:"audience" envalias:"SEARCH_AUTH0_AUDIENCE,AUTH0_AUDIENCE"`

	// Issuer is the JWT issuer claim; defaults to "https://<Domain>/" when empty.
	Issuer string `mapstructure:"issuer" envalias:"SEARCH_AUTH0_ISSUER,AUTH0_ISSUER"`
}

// AuthConfig holds authentication configuration for Go services.
//
// Field order is tuned for struct packing (govet fieldalignment): the large
// fields (the Auth0 struct, the map, the string) lead and the three bool flags
// trail. The mapstructure/envalias tags — not declaration order — drive decoding,
// so this ordering is purely a layout concern, not a semantic one.
type AuthConfig struct {
	// Auth0 holds Auth0 tenant connection parameters used when Stub is false.
	Auth0 Auth0Config `mapstructure:"auth0"`

	// ServiceTokens maps a calling service name to its service-to-service bearer token
	// (Phase 1). The internal WithServiceAuth interceptor validates the caller's
	// `authorization: Bearer <token>` against this per-caller map, so the server records
	// which service called (audit) and can enforce least privilege. Empty in local dev
	// (ServiceStub bypasses validation); provisioned per caller via ExternalSecrets in
	// staging/prod. A value may carry a comma-separated {current,previous} pair to allow a
	// zero-downtime rotation window. The deploy delivers the whole map as one flat
	// "caller=token;caller=token" env var (a Kubernetes secretKeyRef can only carry a
	// scalar); the loader's StringToStringMapHookFunc decodes that into the map. Entries are
	// ";"-separated so a value's rotation-pair "," is preserved (e.g. "ingestion=cur,prev").
	ServiceTokens map[string]string `mapstructure:"service_tokens" envalias:"SERVICE_AUTH_TOKENS"`

	// ServiceToken is THIS service's own outgoing service-to-service credential — the
	// bearer token it presents when calling another internal service (Phase 1),
	// distinct from ServiceTokens (the callers this service accepts). Empty in local dev
	// (sends no credential); provisioned per service via ExternalSecrets in staging/prod.
	ServiceToken string `mapstructure:"service_token" envalias:"SERVICE_AUTH_TOKEN"`

	// Stub enables stub auth validation for local development; must never be true in production.
	Stub bool `mapstructure:"stub" envalias:"STUB_AUTH"`

	// ServiceStub bypasses service-to-service auth for local development (dev
	// bypass), mirroring Stub: the internal interceptor injects a synthetic caller and
	// skips token validation so a local dev stack works without provisioned tokens. Must be
	// false in staging/prod (enforced by Validate when Stub is false + a k8stest guard).
	ServiceStub bool `mapstructure:"service_stub" envalias:"SERVICE_STUB_AUTH"`

	// ServiceAuthAudit runs the service-auth interceptor in audit mode: the caller is
	// resolved and logged but a missing/invalid token is NOT rejected. Used for a staged
	// rollout (deploy in audit, confirm every client sends a valid token, then flip to
	// enforce). Default false (enforce).
	ServiceAuthAudit bool `mapstructure:"service_auth_audit"`
}

// DefaultAuthConfig returns a safe local-dev default with both the end-user and the
// service-to-service auth stubs enabled (no Auth0 or service tokens required locally).
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{Stub: true, ServiceStub: true}
}

// Validate returns an error when stub=false but required Auth0 params are missing, or
// when the service-auth dev bypass is left on in a real (non-stub) deployment.
func (c *AuthConfig) Validate() error {
	// A real deployment (end-user auth enforced) must not leave service-to-service auth
	// bypassed — that would reproduce the F1/F2 unauthenticated internal surface.
	if !c.Stub && c.ServiceStub {
		return coreerr.InvalidInput(
			"auth.service_stub must be false when auth.stub is false",
		)
	}
	if c.Stub {
		return nil
	}
	if c.Auth0.Domain == "" {
		return coreerr.InvalidInput("auth.auth0.domain is required when stub=false")
	}
	if c.Auth0.Audience == "" {
		return coreerr.InvalidInput("auth.auth0.audience is required when stub=false")
	}
	return nil
}
