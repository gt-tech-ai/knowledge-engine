//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	riverclient "github.com/gt-tech-ai/knowledge-engine/go/clients/jobs/river"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/fixtures/dbtest/postgres"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver for the assertion query
)

// TestRiverMigrate_CreatesRiverSchema verifies the worker's River migration step
// creates River's own tables against a real Postgres.
//
// Why this test is important:
//   - A worker's River runtime cannot start until River's schema
//
// (river_job, river_leader, …) exists. applies it via River's own
//
//	migrator (NOT the Ent/Atlas migration dir, which would make `migrate check`
//	report false drift). This proves the migrator runs and creates the tables.
//
// What it tests:
//   - river.Migrate(ctx, dsn) creates the river_job and river_leader tables.
func TestRiverMigrate_CreatesRiverSchema(t *testing.T) {
	ctx := context.Background()
	db, err := postgres.NewTestDatabase(ctx)
	require.NoError(t, err, "start test database")
	defer db.Close(ctx)

	require.NoError(t, riverclient.Migrate(ctx, db.GetDSN()), "apply river schema")

	conn, err := sql.Open("pgx", db.GetDSN())
	require.NoError(t, err)
	defer conn.Close() //nolint:errcheck // test cleanup

	for _, table := range []string{"river_job", "river_leader"} {
		var exists bool
		row := conn.QueryRowContext(
			ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
			table,
		)
		require.NoError(t, row.Scan(&exists))
		require.Truef(t, exists, "table %q must exist after river.Migrate", table)
	}
}
