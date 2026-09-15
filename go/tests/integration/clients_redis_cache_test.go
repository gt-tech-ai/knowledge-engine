//go:build integration

// Package clients_test verifies the Redis cache implementation against a real
// Redis instance (redis:7-alpine via testcontainers). Tests confirm that the
// cache satisfies the interfaces.ByteCache contract and that concrete extensions
// (GetOrLoad population, Ping, TTL expiry) work end-to-end. Unit tests in
// pkg/go/tests/clients/cache_redis_test.go cover error paths using mocks; this
// suite targets correctness over a real connection.
package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	rediscache "github.com/gt-tech-ai/knowledge-engine/go/clients/cache/redis"
	testsuite "github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures/suite"
)

// RedisCacheSuite holds a real Redis container and a fresh Cache client per test.
type RedisCacheSuite struct {
	testsuite.RedisIntegrationSuite
	cache *rediscache.Cache
}

// TestRedisCacheSuite runs all Redis cache integration tests.
//
// Why this test is important:
//   - Without a top-level TestXxx function, `go test` ignores every suite method
//
// What it tests:
//   - Suite entrypoint wires RedisCacheSuite into the testify runner
func TestRedisCacheSuite(t *testing.T) {
	suite.Run(t, new(RedisCacheSuite))
}

func (s *RedisCacheSuite) SetupTest() {
	cfg := rediscache.DefaultConfig()
	cfg.Addr = s.RedisAddr
	cfg.OpTimeout = 2 * time.Second
	s.cache = rediscache.New(&cfg)
}

func (s *RedisCacheSuite) TearDownTest() {
	if s.cache != nil {
		_ = s.cache.Close()
	}
}

// uniqueKey returns a test-scoped cache key to prevent cross-test pollution when
// tests share a single Redis instance.
func (s *RedisCacheSuite) uniqueKey(label string) string {
	return fmt.Sprintf("%s:%s", s.T().Name(), label)
}

// TestPing tests that Cache.Ping confirms the connection to the Redis server.
//
// Why this test is important:
//   - An early signal that the container address is wired correctly;
//     a failed Ping catches misconfiguration before any other test runs
//
// What it tests:
//   - Ping returns no error when Redis is reachable
func (s *RedisCacheSuite) TestPing() {
	err := s.cache.Ping(context.Background())
	s.Require().NoError(err, "Redis should be reachable")
}

// TestSetGet_Hit tests that a value written with Set is returned by Get.
//
// Why this test is important:
//   - This is the fundamental cache-aside happy path the repository decorator
//     relies on; a broken hit means every cached read falls through to the DB
//
// What it tests:
//   - Get returns (value, true) for a key previously written with Set
func (s *RedisCacheSuite) TestSetGet_Hit() {
	ctx := context.Background()
	key := s.uniqueKey("hit")
	want := []byte("hello-redis")

	s.cache.Set(ctx, key, want, time.Minute)

	got, ok := s.cache.Get(ctx, key)
	s.True(ok, "expected cache hit")
	s.Equal(want, got)
}

// TestGet_Miss tests that Get returns (nil, false) for a key that has never
// been written.
//
// Why this test is important:
//   - Callers use the bool to decide whether to invoke the loader; a wrong
//     return here breaks the cache-aside pattern and may surface stale data
//
// What it tests:
//   - Get on an unknown key returns (nil, false)
func (s *RedisCacheSuite) TestGet_Miss() {
	ctx := context.Background()
	key := s.uniqueKey("miss")

	got, ok := s.cache.Get(ctx, key)
	s.False(ok, "expected cache miss for unknown key")
	s.Nil(got)
}

// TestDelete tests that Delete removes a key so subsequent Get returns a miss.
//
// Why this test is important:
//   - This is the invalidation path exercised by repository writes and mutation
//     hooks; a failure means stale entries persist indefinitely
//
// What it tests:
//   - Key is present before Delete and absent after Delete
func (s *RedisCacheSuite) TestDelete() {
	ctx := context.Background()
	key := s.uniqueKey("del")

	s.cache.Set(ctx, key, []byte("to-be-deleted"), time.Minute)
	_, hit := s.cache.Get(ctx, key)
	s.Require().True(hit, "key should exist before Delete")

	s.cache.Delete(ctx, key)

	_, hit = s.cache.Get(ctx, key)
	s.False(hit, "key should be absent after Delete")
}

// TestGetOrLoad_PopulatesOnMiss tests that GetOrLoad invokes the loader on a
// cache miss, stores the result, and returns from cache on the second call.
//
// Why this test is important:
//   - Confirms the singleflight population path that prevents cache stampedes;
//     a loader called more than once indicates the result was not persisted
//
// What it tests:
//   - First call (miss): loader is invoked once and the result is returned
//   - Second call (hit): loader is not invoked again; cached value is returned
func (s *RedisCacheSuite) TestGetOrLoad_PopulatesOnMiss() {
	ctx := context.Background()
	key := s.uniqueKey("load")
	calls := 0

	loader := func() ([]byte, error) {
		calls++
		return []byte("loaded-value"), nil
	}

	// First call: miss, loader invoked.
	got, err := s.cache.GetOrLoad(ctx, key, loader, time.Minute)
	s.Require().NoError(err)
	s.Equal([]byte("loaded-value"), got)
	s.Equal(1, calls, "loader should be called exactly once on miss")

	// Second call: hit, loader not invoked again.
	got2, err := s.cache.GetOrLoad(ctx, key, loader, time.Minute)
	s.Require().NoError(err)
	s.Equal([]byte("loaded-value"), got2)
	s.Equal(1, calls, "loader should not be called again on cache hit")
}

// TestTTLExpiry tests that a key written with a short TTL is unavailable after
// the TTL elapses.
//
// Why this test is important:
//   - TTL enforcement by Redis is the only mechanism preventing stale cache
//     entries; this cannot be validated with mocks or a fake clock
//
// What it tests:
//   - Key is present immediately after Set with a short TTL
//   - Key is absent after the TTL elapses
func (s *RedisCacheSuite) TestTTLExpiry() {
	ctx := context.Background()
	key := s.uniqueKey("ttl")
	ttl := 60 * time.Millisecond

	s.cache.Set(ctx, key, []byte("expires-soon"), ttl)

	_, hit := s.cache.Get(ctx, key)
	s.True(hit, "key should be present immediately after Set")

	time.Sleep(ttl + 100*time.Millisecond)

	_, hit = s.cache.Get(ctx, key)
	s.False(hit, "key should have expired after TTL elapsed")
}
