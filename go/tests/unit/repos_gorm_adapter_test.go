package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
	gormadapter "github.com/gt-tech-ai/knowledge-engine/go/repos/adapters/gorm"
)

// TestGormAdapter_KindAndUnknown tests that the GORM adapter selects the Postgres
// dialect by Kind and rejects an unknown Kind.
//
// Why this test is important:
//   - NewFromConfig is the adapter's app-wiring entrypoint; a silently-accepted
//     unknown Kind would open the wrong (or a nil) dialect, and the Kind string is
//     what logs/metrics report the selected backend as.
//
// What it tests:
//   - KindPostgres stringifies to "postgres" and an out-of-range Kind to "Kind(N)".
//   - NewFromConfig with an unknown Kind returns an error.
func TestGormAdapter_KindAndUnknown(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "postgres", gormadapter.KindPostgres.String())
	assert.Equal(t, "Kind(99)", gormadapter.Kind(99).String())

	_, err := gormadapter.NewFromConfig(gormadapter.Config{Kind: gormadapter.Kind(99)})
	assert.Error(t, err)
}

// TestGormAdapter_LifecycleUnreachable tests the GORM client's not-started guards and
// its fail-fast Start against an unreachable database, mirroring
// TestPostgresBackend_NotStartedGuards / _StartUnreachable: NewFromConfig does no I/O,
// so before Start the pool is unopened and Start surfaces the unreachable host.
//
// Why this test is important:
//   - The adapter is the swappable Ent sibling; a composition root relies on Liveness
//     and Readiness reporting not-ready until Start opens the pool, and on Start failing
//     fast when the database is unreachable rather than deferring the failure to the
//     first query (and NOT doing I/O in the constructor).
//
// What it tests:
//   - The client satisfies interfaces.Client.
//   - NewFromConfig succeeds without dialing (no I/O); before Start, DB() is nil and
//     Liveness/Readiness report not-started, and Stop is a safe no-op.
//   - Start against an unreachable host returns a non-nil error.
func TestGormAdapter_LifecycleUnreachable(t *testing.T) {
	t.Parallel()

	client, err := gormadapter.NewFromConfig(gormadapter.Config{
		Kind: gormadapter.KindPostgres,
		Database: infra.DatabaseConfig{
			Host:     "invalid-host.example.invalid",
			Port:     5432,
			User:     "u",
			Password: "p",
			Database: "d",
			SSLMode:  "disable",
		},
	})
	require.NoError(
		t,
		err,
		"NewFromConfig does no I/O, so an unreachable host is not an error here",
	)
	require.NotNil(t, client)

	var _ interfaces.Client = client
	ctx := context.Background()

	// Before Start: the pool is unopened.
	assert.Nil(t, client.DB(), "the pool is not opened until Start")
	assert.Error(t, client.Liveness(ctx), "not started → not live")
	assert.Error(t, client.Readiness(ctx), "not started → not ready")
	assert.NoError(t, client.Stop(ctx), "Stop before Start is a no-op")

	// Start dials the unreachable host and must fail fast.
	assert.Error(t, client.Start(ctx), "Start against an unreachable host fails fast")
}
