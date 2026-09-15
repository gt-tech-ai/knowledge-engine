package river

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// Migrate applies River's own database schema (the river_job, river_leader, …
// tables the River runtime requires) to the database at databaseURL.
//
// River's tables are infrastructure River owns, not Ent-managed entities, so they
// are applied with River's native migrator rather than the Ent/Atlas migration
// directory — keeping them out of `search migrate check` (which diffs the Ent
// schema against the Atlas migrations and would otherwise report perpetual drift
// for the non-Ent river_* tables). The document-events worker's runtime cannot
// start until these tables exist; the migration Job invokes Migrate after the
// Atlas apply.
//
// Migrate is idempotent: re-running it is a no-op once the schema is current.
func Migrate(ctx context.Context, databaseURL string) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "river migrate: open pool")
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "river migrate: new migrator")
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "river migrate: apply up")
	}
	return nil
}
