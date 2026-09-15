package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/transport/connect/interceptors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// TestRequireUserPermissions tests the shared get-or-401 permissions accessor.
//
// Why this test is important:
//   - Every authenticated RPC gates on the caller's resolved permissions; the contract is "present →
//     return them, absent → a 401 (Unauthorized) with a stable message". A wrong branch would either
//     leak an unauthenticated request into a handler or 401 a valid caller.
//
// What it tests:
//   - A context carrying UserPermissions returns them with no error.
//   - A context without permissions returns a CodeUnauthorized error.
func TestRequireUserPermissions(t *testing.T) {
	t.Parallel()

	perms := &interceptors.UserPermissions{OrgRole: "admin"}
	ctx := interceptors.WithUserPermissions(context.Background(), perms)

	got, err := interceptors.RequireUserPermissions(ctx)
	require.NoError(t, err)
	assert.Same(t, perms, got)

	_, err = interceptors.RequireUserPermissions(context.Background())
	require.Error(t, err)
	assert.Equal(t, errors.CodeUnauthorized, errors.Code(err))
}
