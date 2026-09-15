package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	auth0pkg "github.com/gt-tech-ai/knowledge-engine/go/clients/auth/auth0"
	authstub "github.com/gt-tech-ai/knowledge-engine/go/clients/auth/stub"
	redispkg "github.com/gt-tech-ai/knowledge-engine/go/clients/cache/redis"
	"github.com/gt-tech-ai/knowledge-engine/go/clients/database"
	dbpostgres "github.com/gt-tech-ai/knowledge-engine/go/clients/database/postgres"
	riverpkg "github.com/gt-tech-ai/knowledge-engine/go/clients/jobs/river"
	messagingsqs "github.com/gt-tech-ai/knowledge-engine/go/clients/messaging/sqs"
	"github.com/gt-tech-ai/knowledge-engine/go/clients/rpc"
	grpcbackend "github.com/gt-tech-ai/knowledge-engine/go/clients/rpc/grpc"
	"github.com/gt-tech-ai/knowledge-engine/go/clients/storage"
	"github.com/gt-tech-ai/knowledge-engine/go/clients/transport"
	connectpkg "github.com/gt-tech-ai/knowledge-engine/go/clients/transport/connect"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// TestDatabaseTier_NewFromConfig tests that the database tier selects the Postgres
// backend by Kind, rejects an unknown Kind, and that the pool guards operations
// until it is started.
//
// Why this test is important:
//   - The tier factory is the app-wiring entrypoint; a mis-selected or nil pool, or
//     a pool that answers Ready before Start opens it, breaks every DB caller.
//
// What it tests:
//   - KindPostgres returns a non-nil DatabasePool; an unknown Kind errors.
//   - Before Start, DB() is nil and Liveness/Readiness report not-started.
func TestDatabaseTier_NewFromConfig(t *testing.T) {
	t.Parallel()

	pool, err := database.NewFromConfig(database.Config{
		Kind:     database.KindPostgres,
		Database: &infra.DatabaseConfig{},
	})
	require.NoError(t, err)
	require.NotNil(t, pool)
	assert.Nil(t, pool.DB(), "pool is not opened until Start")
	assert.Error(t, pool.Liveness(context.Background()))
	assert.Error(t, pool.Readiness(context.Background()))
	assert.NoError(
		t,
		pool.Stop(context.Background()),
		"Stop on an unopened pool is a no-op",
	)

	_, err = database.NewFromConfig(database.Config{Kind: database.Kind(99)})
	assert.Error(t, err)
	assert.Equal(t, "postgres", database.KindPostgres.String())
}

// TestRPCTier_NewFromConfig tests that the RPC tier selects the gRPC backend by
// Kind, rejects an unknown Kind, and that the returned client's lifecycle behaves.
//
// Why this test is important:
//   - The RPC client is dialed on Start (lazily by gRPC); a client that reports
//     Ready before Start, or never exposes a Conn, cannot build working stubs.
//
// What it tests:
//   - KindGRPC returns a non-nil RPCClient; an unknown Kind errors.
//   - Before Start: Conn is nil and Liveness/Readiness report not-started.
//   - After Start: Conn is non-nil, Readiness passes (idle is not a failure state),
//     and Stop closes cleanly.
func TestRPCTier_NewFromConfig(t *testing.T) {
	t.Parallel()

	client, err := rpc.NewFromConfig(rpc.Config{
		Kind: rpc.KindGRPC,
		GRPC: grpcbackend.ClientConfig{Target: "localhost:1"},
	})
	require.NoError(t, err)
	require.NotNil(t, client)

	assert.Nil(t, client.Conn(), "not dialed until Start")
	assert.Error(t, client.Liveness(context.Background()))
	assert.Error(t, client.Readiness(context.Background()))

	require.NoError(t, client.Start(context.Background()))
	assert.NotNil(t, client.Conn())
	assert.NoError(t, client.Liveness(context.Background()))
	assert.NoError(t, client.Readiness(context.Background()))
	assert.NoError(t, client.Stop(context.Background()))

	_, err = rpc.NewFromConfig(rpc.Config{Kind: rpc.Kind(99)})
	assert.Error(t, err)
	assert.Equal(t, "grpc", rpc.KindGRPC.String())
}

// TestTransportTier_NewFromConfig tests that the transport tier selects the Connect
// backend by Kind, rejects an unknown Kind, and that the server reports not-ready
// until it is started.
//
// Why this test is important:
//   - The transport is the server's front door; a nil transport or one that reports
//     Ready before it is listening would make Kubernetes route traffic to a dead pod.
//
// What it tests:
//   - KindConnect returns a non-nil ServerTransport; an unknown Kind errors.
//   - Before Start, Readiness reports not-started while Liveness (process up) passes.
func TestTransportTier_NewFromConfig(t *testing.T) {
	t.Parallel()

	srv, err := transport.NewFromConfig(transport.Config{
		Kind:    transport.KindConnect,
		Logger:  fixtures.NopLogger(),
		Connect: connectpkg.DefaultServerConfig(0),
	})
	require.NoError(t, err)
	require.NotNil(t, srv)
	assert.NoError(t, srv.Liveness(context.Background()))
	assert.Error(
		t,
		srv.Readiness(context.Background()),
		"not ready until Start binds the listener",
	)
	assert.NoError(t, srv.Stop(context.Background()), "Stop before Start is a no-op")

	_, err = transport.NewFromConfig(transport.Config{Kind: transport.Kind(99)})
	assert.Error(t, err)
	assert.Equal(t, "connect", transport.KindConnect.String())
}

// TestStatelessClients_LifecycleNoOps tests that the stateless SDK-backed clients
// (SQS, S3, the auth stub) satisfy interfaces.Lifecycle with safe no-op Start/Stop,
// so a lifecycle.Manager can register them uniformly with the connection-oriented
// clients.
//
// Why this test is important:
//   - These clients hold no persistent connection; their Start/Stop must be safe
//     no-ops so uniform registration never errors or blocks shutdown.
//
// What it tests:
//   - Each client's Start and Stop return nil.
func TestStatelessClients_LifecycleNoOps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	pub, err := messagingsqs.NewPublisher(
		messagingsqs.Config{API: mocks.NewMockAPI(ctrl)},
	)
	require.NoError(t, err)

	s3Client, err := storage.New(ctx, storage.Config{API: mocks.NewMockS3API(ctrl)})
	require.NoError(t, err)

	lifecycles := map[string]interfaces.Lifecycle{
		"sqs-publisher": pub,
		"s3-storage":    mustLifecycle(t, s3Client),
		"auth-stub":     authstub.New(),
	}
	for name, lc := range lifecycles {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, lc.Start(ctx))
			assert.NoError(t, lc.Stop(ctx))
		})
	}
}

// mustLifecycle asserts v implements interfaces.Lifecycle and returns it, failing
// the test otherwise — the stateless clients are handed back as their domain
// contract, so the test surfaces the lifecycle seam through a type assertion.
func mustLifecycle(t *testing.T, v any) interfaces.Lifecycle {
	t.Helper()
	lc, ok := v.(interfaces.Lifecycle)
	require.True(t, ok, "%T does not implement interfaces.Lifecycle", v)
	return lc
}

// TestPostgresBackend_NotStartedGuards tests the Postgres backend's not-started
// guards directly (the tier test drives it through the interface; this pins the
// backend's own contract).
//
// Why this test is important:
//   - A pool that returns a usable DB() or passes Readiness before Start would let
//     callers issue queries against a nil handle and panic.
//
// What it tests:
//   - New (unopened): DB() is nil, Liveness and Readiness error, Stop is a no-op.
func TestPostgresBackend_NotStartedGuards(t *testing.T) {
	t.Parallel()

	c := dbpostgres.New(&infra.DatabaseConfig{})
	assert.Nil(t, c.DB())
	assert.Error(t, c.Liveness(context.Background()))
	assert.Error(t, c.Readiness(context.Background()))
	assert.NoError(t, c.Stop(context.Background()))
}

// TestPostgresBackend_StartUnreachable tests that Start surfaces an unreachable
// database as a boot-time error (rather than deferring the failure to the first
// query), by opening the pool and pinging.
//
// Why this test is important:
//   - Fail-fast at the composition root (D11) is the whole point of the pool's
//     Start; a Start that "succeeds" against a dead host hides the outage.
//
// What it tests:
//   - Start against an unresolvable host returns a non-nil error.
func TestPostgresBackend_StartUnreachable(t *testing.T) {
	t.Parallel()

	c := dbpostgres.New(&infra.DatabaseConfig{
		Host:     "invalid-host.example.invalid",
		Port:     5432,
		User:     "u",
		Password: "p",
		Database: "d",
		SSLMode:  "disable",
	})
	assert.Error(t, c.Start(context.Background()))
}

// TestRedisBackend_LifecycleUnreachable tests the Redis cache's lifecycle contract:
// the pool is created eagerly (so Liveness passes) but connectivity is only proven
// on Start/Readiness, which fail against an unreachable server.
//
// Why this test is important:
//   - Liveness (pool exists) and Readiness (server reachable) must answer different
//     questions so Kubernetes restarts vs. deroutes correctly.
//
// What it tests:
//   - Liveness passes (pool constructed); Start and Readiness error against a
//     refused address; Stop closes cleanly.
func TestRedisBackend_LifecycleUnreachable(t *testing.T) {
	t.Parallel()

	cache := redispkg.New(&redispkg.Config{Addr: "127.0.0.1:1", OpTimeout: time.Second})
	lc, ok := any(cache).(interfaces.Client)
	require.True(t, ok)

	assert.NoError(t, lc.Liveness(context.Background()), "the pool is constructed by New")
	assert.Error(t, lc.Start(context.Background()), "a refused address fails the Ping")
	assert.Error(t, lc.Readiness(context.Background()))
	assert.NoError(t, lc.Stop(context.Background()))
}

// TestRiverEnqueuer_LifecycleNoOps tests that the River enqueuer satisfies the
// lifecycle contract with safe no-ops (its pool belongs to the Runtime).
//
// Why this test is important:
//   - The enqueuer must register uniformly with a lifecycle.Manager without owning
//     a connection to open or close.
//
// What it tests:
//   - Start and Stop return nil.
func TestRiverEnqueuer_LifecycleNoOps(t *testing.T) {
	t.Parallel()

	client, err := riverpkg.NewClient(riverpkg.Config{})
	require.NoError(t, err)
	assert.NoError(t, client.Start(context.Background()))
	assert.NoError(t, client.Stop(context.Background()))
}

// TestAuth0Backend_LifecycleNoOps tests that the Auth0 provider satisfies the
// lifecycle contract with safe no-ops (its M2M token is fetched lazily, so there is
// no persistent connection to open or close).
//
// Why this test is important:
//   - The Auth0 client must register uniformly with a lifecycle.Manager alongside
//     the connection-oriented clients.
//
// What it tests:
//   - Start and Stop return nil.
func TestAuth0Backend_LifecycleNoOps(t *testing.T) {
	t.Parallel()

	client := auth0pkg.NewWithSeams(nil, nil, nil, nil, nil, nil)
	assert.NoError(t, client.Start(context.Background()))
	assert.NoError(t, client.Stop(context.Background()))
}
