package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/auth"
)

// TestAuthKind_String tests the auth-tier Kind string forms, including the
// out-of-range fallback.
//
// Why this test is important:
//   - Kind strings surface in config-validation errors and logs; an unknown value
//     must render a diagnosable Kind(N) rather than an empty string so a
//     misconfiguration is legible to an operator.
//
// What it tests:
//   - KindAuth0 → "auth0", KindStub → "stub", and an out-of-range Kind → the numeric
//     fallback.
func TestAuthKind_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "auth0", auth.KindAuth0.String())
	assert.Equal(t, "stub", auth.KindStub.String())
	assert.Equal(t, "Kind(99)", auth.Kind(99).String())
}

// TestAuthNewFromConfig tests the auth-tier factory across every backend selection.
//
// Why this test is important:
//   - NewFromConfig is the app-wiring entrypoint for the identity provider; each Kind
//     must build its backend offline (no external call at construction) and an unknown
//     Kind must fail loudly rather than return a nil provider that panics at first use.
//
// What it tests:
//   - KindStub returns the in-memory provider; KindAuth0 builds the Auth0-backed
//     provider (the M2M token is fetched lazily, so construction stays offline); an
//     out-of-range Kind returns an "unknown auth kind" error.
func TestAuthNewFromConfig(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	stubProvider, err := auth.NewFromConfig(ctx, auth.Config{Kind: auth.KindStub})
	require.NoError(t, err)
	require.NotNil(t, stubProvider)

	auth0Provider, err := auth.NewFromConfig(ctx, auth.Config{
		Kind: auth.KindAuth0,
		Auth0: auth.Auth0Config{
			Domain:           "tenant.us.auth0.com",
			ClientID:         "cid",
			ClientSecret:     "secret",
			PlatformClientID: "platform-app",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, auth0Provider)

	_, err = auth.NewFromConfig(ctx, auth.Config{Kind: auth.Kind(99)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown auth kind")
}
