package unit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rediscache "github.com/gt-tech-ai/knowledge-engine/go/clients/cache/redis"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures"
)

// newTestCache creates a *redis.Cache backed by miniredis for testing.
func newTestCache(
	t *testing.T,
	opts ...func(*rediscache.Config),
) (*rediscache.Cache, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)

	cfg := &rediscache.Config{
		Addr:        mr.Addr(),
		DefaultTTL:  5 * time.Minute,
		OpTimeout:   2 * time.Second,
		FailureMode: rediscache.FailureModeBypass,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return rediscache.New(cfg), mr
}

// TestRedisCache_TLSConfiguresEncryptedClient tests that Config.TLS makes the
// underlying redis client use an encrypted connection.
//
// Why this test is important:
//   - Managed Redis (AWS ElastiCache with an auth token) mandates in-transit
//     encryption; a plaintext client silently i/o-times-out and every cache op
//     bypasses. This guards the wiring that turns TLS on for staging/prod while
//     leaving local dev plaintext.
//
// What it tests:
//   - TLS:true  -> the client's Options().TLSConfig is non-nil
//   - TLS:false -> it is nil (local dev stays plaintext)
func TestRedisCache_TLSConfiguresEncryptedClient(t *testing.T) {
	t.Parallel()

	secure := rediscache.New(&rediscache.Config{Addr: "example:6379", TLS: true})
	require.NotNil(
		t,
		secure.Client().Options().TLSConfig,
		"TLS:true must configure an encrypted client",
	)

	plain := rediscache.New(&rediscache.Config{Addr: "example:6379"})
	require.Nil(
		t,
		plain.Client().Options().TLSConfig,
		"TLS:false must leave the client plaintext",
	)
}

// ---------------------------------------------------------------------------
// Get - cache miss
// ---------------------------------------------------------------------------

// TestRedisCache_GetMiss tests that Get returns (nil, false) for a key that
// does not exist.
//
// Why this test is important:
//   - Cache misses are the default state for all new keys; the return value
//     must be (nil, false) to allow callers to distinguish misses from hits
//
// What it tests:
//   - Get on a non-existent key returns (nil, false)
func TestRedisCache_GetMiss(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	ctx := context.Background()

	val, ok := c.Get(ctx, "nonexistent")
	require.False(t, ok, "expected cache miss for nonexistent key")
	assert.Nil(t, val)
}

// ---------------------------------------------------------------------------
// Set then Get - round trip
// ---------------------------------------------------------------------------

// TestRedisCache_SetThenGet tests that Set followed by Get returns the stored
// bytes.
//
// Why this test is important:
//   - This is the fundamental read-after-write contract; if it breaks, all
//     cache usage across the platform is broken
//
// What it tests:
//   - Set stores bytes, Get retrieves them with ok=true
func TestRedisCache_SetThenGet(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	ctx := context.Background()

	c.Set(ctx, "key1", []byte("value1"), time.Minute)

	val, ok := c.Get(ctx, "key1")
	require.True(t, ok, "expected cache hit after Set")
	assert.Equal(t, "value1", string(val))
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

// TestRedisCache_Delete tests that Delete removes a key from the cache.
//
// Why this test is important:
//   - Cache invalidation must actually remove data; a no-op Delete would serve
//     stale data indefinitely
//
// What it tests:
//   - Delete removes the key, subsequent Get returns miss
func TestRedisCache_Delete(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	ctx := context.Background()

	c.Set(ctx, "key1", []byte("value1"), time.Minute)
	c.Delete(ctx, "key1")

	_, ok := c.Get(ctx, "key1")
	require.False(t, ok, "expected cache miss after Delete")
}

// ---------------------------------------------------------------------------
// Default TTL
// ---------------------------------------------------------------------------

// TestRedisCache_DefaultTTL tests that Set with zero TTL uses the configured
// default.
//
// Why this test is important:
//   - Many callers pass zero TTL expecting the configured default; if zero
//     means "no expiry," Redis memory grows unbounded
//
// What it tests:
//   - Set with ttl=0 applies the default TTL (key expires)
func TestRedisCache_DefaultTTL(t *testing.T) {
	t.Parallel()

	c, mr := newTestCache(t, func(cfg *rediscache.Config) {
		cfg.DefaultTTL = 30 * time.Second
	})
	ctx := context.Background()

	c.Set(ctx, "key1", []byte("value1"), 0)

	// Verify the key has a TTL set
	ttl := mr.TTL("key1")
	assert.NotZero(t, ttl, "expected non-zero TTL from default, got 0")
	assert.LessOrEqual(t, ttl, 30*time.Second)
}

// ---------------------------------------------------------------------------
// Explicit TTL
// ---------------------------------------------------------------------------

// TestRedisCache_ExplicitTTL tests that Set with a non-zero TTL uses that
// value.
//
// Why this test is important:
//   - Different data types need different TTLs (e.g., session=30m, config=1h);
//     explicit TTL must override the default
//
// What it tests:
//   - Set with explicit TTL=60s applies that TTL (not the default)
func TestRedisCache_ExplicitTTL(t *testing.T) {
	t.Parallel()

	c, mr := newTestCache(t, func(cfg *rediscache.Config) {
		cfg.DefaultTTL = 5 * time.Minute
	})
	ctx := context.Background()

	c.Set(ctx, "key1", []byte("value1"), 60*time.Second)

	ttl := mr.TTL("key1")
	assert.LessOrEqual(t, ttl, 60*time.Second)
	assert.GreaterOrEqual(t, ttl, 50*time.Second, "TTL should be close to 60s")
}

// ---------------------------------------------------------------------------
// GetOrLoad - cache miss
// ---------------------------------------------------------------------------

// TestRedisCache_GetOrLoad_CacheMiss tests that GetOrLoad calls the loader on a
// cache miss and populates the cache.
//
// Why this test is important:
//   - GetOrLoad is the primary read-through cache pattern; on miss it must
//     call the loader exactly once and store the result
//
// What it tests:
//   - Loader is called on miss
//   - Value is returned
//   - Subsequent Get returns the cached value
func TestRedisCache_GetOrLoad_CacheMiss(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	ctx := context.Background()

	loaderCalled := 0
	val, err := c.GetOrLoad(ctx, "load-key", func() ([]byte, error) {
		loaderCalled++
		return []byte("loaded-value"), nil
	}, time.Minute)
	require.NoError(t, err, "GetOrLoad")
	assert.Equal(t, "loaded-value", string(val))
	assert.Equal(t, 1, loaderCalled)

	// Verify the value was cached.
	cached, ok := c.Get(ctx, "load-key")
	require.True(t, ok, "expected cache hit after GetOrLoad")
	assert.Equal(t, "loaded-value", string(cached))
}

// ---------------------------------------------------------------------------
// GetOrLoad - cache hit
// ---------------------------------------------------------------------------

// TestRedisCache_GetOrLoad_CacheHit tests that GetOrLoad returns the cached
// value without calling the loader.
//
// Why this test is important:
//   - On cache hit, the loader must not be invoked; calling it would negate
//     the performance benefit of caching
//
// What it tests:
//   - Pre-populated key returns cached value
//   - Loader is never called
func TestRedisCache_GetOrLoad_CacheHit(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	ctx := context.Background()

	c.Set(ctx, "hit-key", []byte("cached"), time.Minute)

	loaderCalled := false
	val, err := c.GetOrLoad(ctx, "hit-key", func() ([]byte, error) {
		loaderCalled = true
		return []byte("should-not-be-used"), nil
	}, time.Minute)
	require.NoError(t, err, "GetOrLoad")
	assert.Equal(t, "cached", string(val))
	assert.False(t, loaderCalled, "loader should not be called on cache hit")
}

// ---------------------------------------------------------------------------
// GetOrLoad - loader error
// ---------------------------------------------------------------------------

// TestRedisCache_GetOrLoad_LoaderError tests that GetOrLoad propagates loader
// errors and does not populate the cache.
//
// Why this test is important:
//   - Caching a failed load would serve error data to all subsequent callers;
//     the error must be propagated and the cache left empty
//
// What it tests:
//   - Loader error is returned to caller
//   - Cache is not populated
func TestRedisCache_GetOrLoad_LoaderError(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	ctx := context.Background()

	val, err := c.GetOrLoad(ctx, "error-key", func() ([]byte, error) {
		return nil, fmt.Errorf("db connection failed")
	}, time.Minute)

	require.Error(t, err, "expected error from loader")
	assert.Nil(t, val)

	// Cache should not be populated.
	_, ok := c.Get(ctx, "error-key")
	assert.False(t, ok, "cache should not be populated after loader error")
}

// ---------------------------------------------------------------------------
// Ping
// ---------------------------------------------------------------------------

// TestRedisCache_Ping tests that Ping returns nil on a healthy Redis.
//
// Why this test is important:
//   - Health checks use Ping to verify Redis connectivity; a broken Ping
//     would cause false-negative health checks
//
// What it tests:
//   - Ping returns nil when Redis is reachable
func TestRedisCache_Ping(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	ctx := context.Background()

	require.NoError(t, c.Ping(ctx), "Ping")
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

// TestRedisCache_Close tests that Close succeeds.
//
// Why this test is important:
//   - Resource cleanup must complete without error; a broken Close leaks
//     connections and file descriptors
//
// What it tests:
//   - Close returns nil
func TestRedisCache_Close(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)

	require.NoError(t, c.Close(), "Close")
}

// ---------------------------------------------------------------------------
// FailureMode - bypass
// ---------------------------------------------------------------------------

// TestRedisCache_FailureModeBypas tests that Redis errors are silently absorbed
// in bypass mode, returning a cache miss.
//
// Why this test is important:
//   - In bypass mode, cache unavailability should degrade gracefully to
//     misses rather than failing requests; this is the default production
//     behavior
//
// What it tests:
//   - After closing Redis, Get returns (nil, false) without error
func TestRedisCache_FailureModeBypas(t *testing.T) {
	t.Parallel()

	c, mr := newTestCache(t, func(cfg *rediscache.Config) {
		cfg.FailureMode = rediscache.FailureModeBypass
	})
	ctx := context.Background()

	// Set a value, then close miniredis to simulate failure.
	c.Set(ctx, "key1", []byte("value1"), time.Minute)
	mr.Close()

	val, ok := c.Get(ctx, "key1")
	require.False(t, ok, "expected miss when Redis is unavailable in bypass mode")
	assert.Nil(t, val)
}

// ---------------------------------------------------------------------------
// FailureMode - error (logs)
// ---------------------------------------------------------------------------

// TestRedisCache_FailureModeError tests that Redis errors are logged in error
// mode while still returning a cache miss.
//
// Why this test is important:
//   - In error mode, operators need visibility into cache failures for
//     alerting and debugging; the error must be logged
//   - ByteCache contract still requires absorbing the error (returning miss)
//
// What it tests:
//   - After closing Redis, Get returns (nil, false)
//   - Set with unavailable Redis does not panic
func TestRedisCache_FailureModeError(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	c, mr := newTestCache(t, func(cfg *rediscache.Config) {
		cfg.FailureMode = rediscache.FailureModeError
		cfg.Logger = spy
	})
	ctx := context.Background()

	mr.Close()

	// Get should return miss and log the error.
	val, ok := c.Get(ctx, "key1")
	require.False(t, ok, "expected miss when Redis is unavailable")
	assert.Nil(t, val)

	// Set should not panic.
	c.Set(ctx, "key2", []byte("value2"), time.Minute)

	// Delete should not panic.
	c.Delete(ctx, "key3")
}

// ---------------------------------------------------------------------------
// New with nil logger (cfg.Logger == nil)
// ---------------------------------------------------------------------------

// TestRedisCache_NilLoggerDoesNotPanic tests that creating a cache with a nil
// logger and using FailureModeError does not panic when Redis is unreachable.
// This exercises the nil-logger guard in New (redis/cache.go line 107 path)
// and the `c.logger != nil` checks in Get/Set/Delete.
//
// Why this test is important:
//   - When Logger is nil and FailureMode is Error, the cache must silently
//     skip error logging rather than nil-pointer-dereference panic
//
// What it tests:
//   - New with nil Logger succeeds
//   - Get/Set/Delete on unavailable Redis with nil Logger + FailureModeError
//     do not panic
func TestRedisCache_NilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	c, mr := newTestCache(t, func(cfg *rediscache.Config) {
		cfg.Logger = nil
		cfg.FailureMode = rediscache.FailureModeError
	})
	ctx := context.Background()

	// Close miniredis to simulate Redis failure
	mr.Close()

	// These must not panic despite nil logger + FailureModeError
	val, ok := c.Get(ctx, "any-key")
	require.False(t, ok, "expected miss when Redis is unavailable")
	assert.Nil(t, val)

	c.Set(ctx, "any-key", []byte("data"), time.Minute)
	c.Delete(ctx, "any-key")
}

// ---------------------------------------------------------------------------
// Zero OpTimeout (opCtx returns parent context unchanged)
// ---------------------------------------------------------------------------

// TestRedisCache_ZeroOpTimeout tests that a cache created with OpTimeout=0
// inherits the caller's context deadline and works correctly. This exercises
// the else-branch in opCtx (redis/cache.go line 135) where opTimeout is zero
// and the parent context is returned unchanged.
//
// Why this test is important:
//   - When OpTimeout is zero, opCtx must return the parent context unchanged
//     (no timeout wrapper); if it mistakenly wraps with zero timeout, all ops
//     would immediately deadline-exceed
//
// What it tests:
//   - Set + Get round-trip succeeds with zero OpTimeout
//   - Delete works with zero OpTimeout
func TestRedisCache_ZeroOpTimeout(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t, func(cfg *rediscache.Config) {
		cfg.OpTimeout = 0
	})
	ctx := context.Background()

	// Set + Get round-trip
	c.Set(ctx, "key1", []byte("value1"), time.Minute)

	val, ok := c.Get(ctx, "key1")
	require.True(t, ok, "expected cache hit after Set with zero OpTimeout")
	assert.Equal(t, "value1", string(val))

	// Delete
	c.Delete(ctx, "key1")

	_, ok = c.Get(ctx, "key1")
	require.False(t, ok, "expected cache miss after Delete with zero OpTimeout")
}

// ---------------------------------------------------------------------------
// DefaultTTL zero in config (New applies 5m default)
// ---------------------------------------------------------------------------

// TestRedisCache_NewDefaultTTLZero tests that New applies a 5-minute default
// when cfg.DefaultTTL is zero. This exercises the DefaultTTL==0 guard at the
// top of New (redis/cache.go line 107-109).
//
// Why this test is important:
//   - If DefaultTTL stays zero, Redis SET with zero expiry means no expiry,
//     which would cause unbounded memory growth in production
//
// What it tests:
//   - New with DefaultTTL=0 results in a working cache
//   - Set with ttl=0 applies the fallback default TTL (key gets expiry)
func TestRedisCache_NewDefaultTTLZero(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	cfg := &rediscache.Config{
		Addr:        mr.Addr(),
		DefaultTTL:  0, // trigger the guard: if cfg.DefaultTTL == 0
		OpTimeout:   time.Second,
		FailureMode: rediscache.FailureModeBypass,
	}
	c := rediscache.New(cfg)
	ctx := context.Background()

	c.Set(ctx, "key1", []byte("value1"), 0) // ttl=0 -> uses defaultTTL

	// Verify the key has a TTL (the default 5m was applied)
	ttl := mr.TTL("key1")
	assert.NotZero(t, ttl, "expected non-zero TTL from default fallback, got 0")
	assert.LessOrEqual(t, ttl, 5*time.Minute)

	// Verify the value is retrievable
	val, ok := c.Get(ctx, "key1")
	require.True(t, ok, "expected cache hit")
	assert.Equal(t, "value1", string(val))
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

// TestRedisCache_DefaultConfig tests that DefaultConfig returns sensible
// non-zero defaults for connecting to a local Redis instance.
//
// Why this test is important:
//   - Services that omit explicit cache config inherit these defaults; zero or
//     invalid defaults (e.g., empty Addr) would fail to connect silently
//
// What it tests:
//   - Addr is "localhost:6379", DefaultTTL is 5m, OpTimeout is 500ms, PoolSize >= 1
func TestRedisCache_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := rediscache.DefaultConfig()
	assert.Equal(t, "localhost:6379", cfg.Addr)
	assert.Equal(t, 5*time.Minute, cfg.DefaultTTL)
	assert.Equal(t, 500*time.Millisecond, cfg.OpTimeout)
	assert.GreaterOrEqual(t, cfg.PoolSize, 1)
}

// ---------------------------------------------------------------------------
// Client accessor
// ---------------------------------------------------------------------------

// TestRedisCache_ClientAccessor tests that Client() returns a non-nil
// underlying Redis client.
//
// Why this test is important:
//   - Advanced callers (e.g., pipeline and subscription operations) bypass the
//     cache interface and use the raw client directly; a nil client panics on use
//
// What it tests:
//   - c.Client() returns a non-nil *redis.Client after construction
func TestRedisCache_ClientAccessor(t *testing.T) {
	t.Parallel()

	c, _ := newTestCache(t)
	client := c.Client()
	require.NotNil(t, client, "expected non-nil redis.Client from Client()")
}

// ---------------------------------------------------------------------------
// InvalidatePrefix
// ---------------------------------------------------------------------------

// TestRedisCache_InvalidatePrefix tests prefix-scoped invalidation: matching
// keys are removed, non-matching keys survive, glob metacharacters in the
// prefix are escaped (so an attacker-influenced org id can't widen the match),
// and an empty/whitespace prefix is a guarded no-op (never a full flush).
//
// Why this test is important:
//   - This is the cache-busting primitive the SQS invalidation consumer calls;
//     an over-broad match would wipe unrelated keys on a shared Redis, and an
//     unescaped prefix turns a hostile org id into a wildcard
//
// What it tests:
//   - Keys under the prefix are deleted; keys outside it (incl. a different org
//     and an unrelated namespace) survive
//   - A prefix containing a Redis glob metacharacter ("*") matches only the
//     literal key, not everything
//   - An empty or whitespace-only prefix deletes nothing and returns nil
func TestRedisCache_InvalidatePrefix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("deletes matching, preserves others", func(t *testing.T) {
		t.Parallel()
		c, _ := newTestCache(t)
		c.Set(ctx, "usercontext:org1:a", []byte("1"), time.Minute)
		c.Set(ctx, "usercontext:org1:b", []byte("2"), time.Minute)
		c.Set(ctx, "usercontext:org2:a", []byte("3"), time.Minute)
		c.Set(ctx, "other:key", []byte("4"), time.Minute)

		require.NoError(t, c.InvalidatePrefix(ctx, "usercontext:org1:"))

		_, ok := c.Get(ctx, "usercontext:org1:a")
		assert.False(t, ok, "org1:a should be invalidated")
		_, ok = c.Get(ctx, "usercontext:org1:b")
		assert.False(t, ok, "org1:b should be invalidated")
		_, ok = c.Get(ctx, "usercontext:org2:a")
		assert.True(t, ok, "org2:a (different org) must survive")
		_, ok = c.Get(ctx, "other:key")
		assert.True(t, ok, "unrelated namespace must survive")
	})

	t.Run("escapes glob metacharacters", func(t *testing.T) {
		t.Parallel()
		c, _ := newTestCache(t)
		c.Set(ctx, "lit*:1", []byte("star"), time.Minute)
		c.Set(ctx, "litERAL:1", []byte("literal"), time.Minute)

		// Unescaped, MATCH "lit*:*" would also match "litERAL:1"; escaping the
		// "*" restricts the match to the literal key.
		require.NoError(t, c.InvalidatePrefix(ctx, "lit*:"))

		_, ok := c.Get(ctx, "lit*:1")
		assert.False(t, ok, "literal-star key should be invalidated")
		_, ok = c.Get(ctx, "litERAL:1")
		assert.True(t, ok, "non-matching key must survive (metachar escaped)")
	})

	t.Run("empty/whitespace prefix is a no-op", func(t *testing.T) {
		t.Parallel()
		c, _ := newTestCache(t)
		c.Set(ctx, "k1", []byte("v"), time.Minute)

		require.NoError(t, c.InvalidatePrefix(ctx, ""))
		require.NoError(t, c.InvalidatePrefix(ctx, "   "))

		_, ok := c.Get(ctx, "k1")
		assert.True(t, ok, "empty/whitespace prefix must not flush the cache")
	})
}

// ---------------------------------------------------------------------------
// InvalidateAll
// ---------------------------------------------------------------------------

// TestRedisCache_InvalidateAll tests that InvalidateAll clears the cache's DB.
//
// Why this test is important:
//   - InvalidateAll completes the interfaces.CacheInvalidator contract; even
//     though the invalidation feature never calls it, it must actually clear
//     keys (a no-op would be a silent lie to any future caller)
//
// What it tests:
//   - After InvalidateAll, previously set keys are gone
func TestRedisCache_InvalidateAll(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	c, _ := newTestCache(t)
	c.Set(ctx, "k1", []byte("v1"), time.Minute)
	c.Set(ctx, "k2", []byte("v2"), time.Minute)

	require.NoError(t, c.InvalidateAll(ctx))

	_, ok := c.Get(ctx, "k1")
	assert.False(t, ok, "k1 should be cleared by InvalidateAll")
	_, ok = c.Get(ctx, "k2")
	assert.False(t, ok, "k2 should be cleared by InvalidateAll")
}
