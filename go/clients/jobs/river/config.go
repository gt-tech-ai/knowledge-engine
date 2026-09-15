// Package river provides River-based job queue integration.
// Full implementation requires PostgreSQL via pgxpool — deferred to Phase 3.
package river

// Config holds River-specific job queue configuration.
type Config struct {
	// DatabaseURL is the PostgreSQL connection string for the job queue.
	// Populated in Phase 3 when PostgreSQL is configured.
	DatabaseURL string
}
