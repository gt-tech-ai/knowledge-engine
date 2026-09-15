package river

import (
	"context"
	"maps"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// defaultMaxConns caps the River pgxpool conservatively. The worker also holds
// a separate database/sql (Ent) pool to the same Postgres, so both pools must
// stay small enough that replicaCount × (river + ent) connections fit
// max_connections.
const defaultMaxConns int32 = 8

// defaultMaxWorkers is the per-queue concurrency when the caller does not set
// one.
const defaultMaxWorkers = 10

// RuntimeConfig configures the River runtime.
type RuntimeConfig struct {
	// Workers is the registry of job-kind handlers River dispatches to.
	Workers *river.Workers

	// ExtraQueues are ADDITIONAL named queues merged onto the base queue
	// (QueueName), each with its own MaxWorkers cap. A worker that serves more
	// than one job kind at DIFFERENT concurrency ceilings (e.g. the
	// document-events sweeps on the default queue + connector syncs on the
	// dedicated connector-sync queue at MaxWorkers=K, the admission
	// gate) supplies them here.
	ExtraQueues map[string]river.QueueConfig

	// DatabaseURL is the pgx connection string for the runtime's dedicated
	// pool.
	DatabaseURL string

	// QueueName is the base queue the runtime works; empty defaults to
	// river.QueueDefault.
	QueueName string

	// PeriodicJobs are scheduled jobs fired once cluster-wide via River leader
	// election.
	PeriodicJobs []*river.PeriodicJob

	// MaxWorkers caps concurrency on the base queue; non-positive uses
	// defaultMaxWorkers.
	MaxWorkers int

	// MaxConns caps the pgxpool size; non-positive uses defaultMaxConns.
	MaxConns int32
}

// Runtime is the real River background-job runtime: a pgxpool-backed
// river.Client with its lifecycle (Start/Stop) and connection pool. Leader
// election (the river_leader table) is built in, so periodic jobs fire once
// cluster-wide across replicas sharing this database.
type Runtime struct {
	// pool is the dedicated pgxpool River runs on (closed by Stop).
	pool *pgxpool.Pool

	// client is the underlying River client.
	client *river.Client[pgx.Tx]
}

// NewRuntime builds a River runtime from the config, opening a capped pgxpool
// and constructing the River client with the caller's workers + periodic jobs
// on the default queue. It does not start processing — call Start.
func NewRuntime(ctx context.Context, cfg RuntimeConfig) (*Runtime, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, errors.Wrap(
			err,
			errors.CodeInternal,
			"river runtime: parse pool config",
		)
	}
	poolCfg.MaxConns = defaultMaxConns
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "river runtime: open pool")
	}

	queues := buildQueues(cfg.QueueName, cfg.MaxWorkers)
	maps.Copy(queues, cfg.ExtraQueues)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       queues,
		Workers:      cfg.Workers,
		PeriodicJobs: cfg.PeriodicJobs,
	})
	if err != nil {
		pool.Close()
		return nil, errors.Wrap(err, errors.CodeInternal, "river runtime: new client")
	}
	return &Runtime{pool: pool, client: client}, nil
}

// buildQueues maps the runtime onto its single queue: queueName (empty → river.QueueDefault) capped at
// maxWorkers (non-positive → defaultMaxWorkers). A dedicated worker's MaxWorkers on its own named queue
// is the GLOBAL admission cap for that job kind. Pure so the construction is unit-tested
// without a database (the ≤K-concurrent behavior itself is proven by the River+Postgres integration).
func buildQueues(queueName string, maxWorkers int) map[string]river.QueueConfig {
	if queueName == "" {
		queueName = river.QueueDefault
	}
	if maxWorkers <= 0 {
		maxWorkers = defaultMaxWorkers
	}
	return map[string]river.QueueConfig{queueName: {MaxWorkers: maxWorkers}}
}

// Start begins fetching and working jobs (and scheduling periodics on the leader).
func (r *Runtime) Start(ctx context.Context) error {
	if err := r.client.Start(ctx); err != nil {
		return errors.Wrap(err, errors.CodeInternal, "river runtime: start")
	}
	return nil
}

// Stop gracefully drains in-flight jobs (honoring ctx for the deadline), then
// closes the connection pool.
func (r *Runtime) Stop(ctx context.Context) error {
	err := r.client.Stop(ctx)
	r.pool.Close()
	if err != nil {
		return errors.Wrap(err, errors.CodeInternal, "river runtime: stop")
	}
	return nil
}

// Client exposes the underlying River client (e.g. for transactional enqueue).
func (r *Runtime) Client() *river.Client[pgx.Tx] { return r.client }
