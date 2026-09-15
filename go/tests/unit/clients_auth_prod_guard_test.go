package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/auth"
	coreerrors "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
)

// TestCreateUser_RefusedInProd tests that the auth factory's prod-guard decorator refuses
// user provisioning in a production environment.
//
// Why this test is important:
//   - Story AC #2 (audit T11): user provisioning must be fail-closed against prod. The guard
//     is defense-in-depth so a misconfigured prod deploy can never mint users through this
//     path — a refusal here must be a coded error, and no user may be created.
//
// What it tests:
//   - A provider built with Env=prod/production refuses CreateUser with a coded Forbidden
//     (the stub inner is never reached — a delegated call would return an id, not an error);
//     a dev environment delegates and returns a user id.
func TestCreateUser_RefusedInProd(t *testing.T) {
	t.Parallel()

	spec := types.UserCreate{
		Email:      "x@example.com",
		Connection: "c",
		RoleIDs:    []string{"rol_x"},
	}

	for _, env := range []string{"prod", "production", "PROD"} {
		prov, err := auth.NewFromConfig(
			context.Background(),
			auth.Config{Kind: auth.KindStub, Env: env},
		)
		require.NoError(t, err)
		id, err := prov.CreateUser(context.Background(), spec)
		require.Errorf(t, err, "env %q must refuse", env)
		assert.Equal(
			t,
			coreerrors.CodeForbidden,
			coreerrors.Code(err),
			"env %q refusal must be coded Forbidden",
			env,
		)
		assert.Empty(t, id, "no user id when refused")
	}

	// Fail-CLOSED: prod-like variants and any unrecognized env are refused too — not only the
	// exact "prod"/"production" tokens (the guard is an allow-list, not a two-token deny-list).
	for _, env := range []string{"prod-us", "production-1", "live", "prd", "staging-prod", "unknown-env"} {
		prov, err := auth.NewFromConfig(
			context.Background(),
			auth.Config{Kind: auth.KindStub, Env: env},
		)
		require.NoError(t, err)
		_, err = prov.CreateUser(context.Background(), spec)
		assert.Errorf(t, err, "fail-closed: env %q must be refused", env)
	}

	// Only the allow-listed non-prod envs (incl. "" — SEARCH_ENV unset in local dev) delegate.
	for _, env := range []string{"", "dev", "development", "staging", "test", "local"} {
		prov, err := auth.NewFromConfig(
			context.Background(),
			auth.Config{Kind: auth.KindStub, Env: env},
		)
		require.NoError(t, err)
		id, err := prov.CreateUser(context.Background(), spec)
		require.NoErrorf(t, err, "allow-listed env %q must delegate", env)
		assert.NotEmptyf(t, id, "env %q delegates and returns a user id", env)
	}
}
