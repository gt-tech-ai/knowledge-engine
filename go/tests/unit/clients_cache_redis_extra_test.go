package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coreerrors "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// TestRedisCache_Invalidation_SurfacesBackendErrors tests that the CacheInvalidator
// surface returns a transient-tagged error when Redis is unreachable, rather than
// silently reporting success.
//
// Why this test is important:
//   - InvalidatePrefix/InvalidateAll back the authorization-cache eviction path; a
//     swallowed Redis error would leave a caller believing stale entries were purged
//     when they were not, so the error must surface (tagged Unavailable) for retry.
//
// What it tests:
//   - With the backing Redis down, InvalidatePrefix and InvalidateAll each return a
//     non-nil error classified Unavailable.
func TestRedisCache_Invalidation_SurfacesBackendErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	c, mr := newTestCache(t)
	mr.Close() // simulate an unreachable Redis for every subsequent command

	prefixErr := c.InvalidatePrefix(ctx, "tenant:42:")
	require.Error(t, prefixErr, "a scan failure must surface, not be swallowed")
	assert.Equal(t, coreerrors.CodeUnavailable, coreerrors.Code(prefixErr))

	allErr := c.InvalidateAll(ctx)
	require.Error(t, allErr, "a flushdb failure must surface")
	assert.Equal(t, coreerrors.CodeUnavailable, coreerrors.Code(allErr))
}

// TestRedisCache_GetOrLoad_NilValue tests that GetOrLoad returns (nil, nil) when the
// loader yields no bytes on a cache miss.
//
// Why this test is important:
//   - A loader may legitimately resolve "no value" (e.g. a soft-missing record); the
//     cache must pass that nil through cleanly rather than panicking on the type
//     assertion of a nil interface value.
//
// What it tests:
//   - On a cache miss, a loader returning (nil, nil) makes GetOrLoad return (nil, nil).
func TestRedisCache_GetOrLoad_NilValue(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	val, err := c.GetOrLoad(context.Background(), "absent",
		func() ([]byte, error) { return nil, nil }, 0)
	require.NoError(t, err)
	assert.Nil(t, val, "a nil loader result passes through as a nil value")
}
