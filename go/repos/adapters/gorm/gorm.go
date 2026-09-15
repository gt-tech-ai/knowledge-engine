// Package gorm is the GORM ORM adapter (repos/adapters/gorm): the swappable
// sibling of repos/adapters/ent. It opens a *gorm.DB selected by Kind (lazily, in
// Start), so a service can switch its persistence backend from Ent to GORM as a
// composition-root wiring change rather than a rewrite. Ent is the default adapter
// today; this establishes the GORM slot with a working, lifecycle-managed opener.
package gorm

import (
	"context"
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	coreerrors "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
)

// Kind selects the SQL dialect the GORM adapter opens.
type Kind int

const (
	// KindPostgres opens a PostgreSQL connection via the GORM postgres driver.
	KindPostgres Kind = iota
)

// String returns the string form of Kind.
func (k Kind) String() string {
	switch k {
	case KindPostgres:
		return "postgres"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config configures the GORM adapter: the SQL dialect (Kind) and the connection
// settings.
type Config struct {
	// Database holds the connection settings (DSN, pool sizing).
	Database infra.DatabaseConfig

	// Kind selects the SQL dialect. The zero value is KindPostgres.
	Kind Kind
}

// Client is a GORM database handle. It mirrors the Ent adapter's client so a
// composition root can select either ORM by configuration. The pool (db) is opened
// lazily in Start — NewFromConfig does no I/O — so the two swappable siblings share
// the same "construct with zero I/O, dial at Start" contract as repos/adapters (and
// the clients/database/postgres backend).
type Client struct {
	// db is the underlying GORM connection pool. It is nil until Start opens it.
	db *gorm.DB
	// cfg holds the dialect (Kind) and connection settings used to dial in Start.
	cfg Config
}

// NewFromConfig validates cfg.Kind and returns an unopened GORM client. It is the
// adapter's app-wiring entrypoint (mothership repos/adapters shape) and does no I/O:
// the connection is dialed in Start so a composition root can be assembled against an
// unreachable database without failing at graph-build time (charter DoD).
func NewFromConfig(cfg Config) (*Client, error) {
	if cfg.Kind != KindPostgres {
		return nil, coreerrors.New(
			coreerrors.CodeInvalidInput,
			fmt.Sprintf("unknown gorm kind: %v", cfg.Kind),
		)
	}
	return &Client{cfg: cfg}, nil
}

// DB returns the underlying *gorm.DB for repositories to query through. It is nil
// until Start has opened the pool.
func (c *Client) DB() *gorm.DB { return c.db }

// Close closes the underlying connection pool (a no-op if never started).
func (c *Client) Close() error {
	if c.db == nil {
		return nil
	}
	sqlDB, err := c.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// compile-time check: the GORM client is a lifecycle-managed, health-checkable
// client (interfaces.Client), so a lifecycle.Manager coordinates it uniformly with
// its Ent sibling and the other clients.
var _ interfaces.Client = (*Client)(nil)

// Start opens the connection pool and verifies it is reachable, failing fast at the
// composition root rather than on the first query.
func (c *Client) Start(ctx context.Context) error {
	db, err := gorm.Open(postgres.Open(c.cfg.Database.DSN()), &gorm.Config{})
	if err != nil {
		return coreerrors.Wrap(err, coreerrors.CodeInternal, "open gorm postgres")
	}
	c.db = db
	return c.Readiness(ctx)
}

// Stop closes the underlying connection pool.
func (c *Client) Stop(_ context.Context) error {
	return c.Close()
}

// Liveness reports whether the pool has been opened (a lightweight self-check that
// does not touch the database); it fails until Start has run.
func (c *Client) Liveness(_ context.Context) error {
	if c.db == nil {
		return coreerrors.Internal("gorm client not started")
	}
	return nil
}

// Readiness verifies the database can serve traffic (a bounded Ping); it fails until
// Start has opened the pool.
func (c *Client) Readiness(ctx context.Context) error {
	if c.db == nil {
		return coreerrors.Internal("gorm client not started")
	}
	sqlDB, err := c.db.DB()
	if err != nil {
		return coreerrors.Wrap(err, coreerrors.CodeInternal, "gorm sql.DB")
	}
	return sqlDB.PingContext(ctx)
}
