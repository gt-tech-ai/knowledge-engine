package river

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.JobEnqueuer = (*Client)(nil)

// Client is a minimal River job client interface.
// Full implementation requires pgxpool.Pool which is not available in Phase 1.
type Client struct {
	// pool *pgxpool.Pool // Deferred to Phase 3 when PostgreSQL is configured
}

// NewClient creates a new River job client.
// Phase 1: Returns stub client. Phase 3: Accept pgxpool.Pool parameter.
func NewClient(cfg Config) (*Client, error) {
	// Phase 1: Stub implementation
	// Phase 3: Initialize River client with PostgreSQL pool
	return &Client{}, nil
}

// Enqueue enqueues a job for processing.
// Phase 1: No-op stub. Phase 3: Use River to enqueue job.
func (c *Client) Enqueue(ctx context.Context, job interfaces.Job) error {
	// Phase 1: Stub — no actual queueing
	// Phase 3: c.river.Insert(ctx, job, nil)
	return nil
}

// Close closes the job client.
func (c *Client) Close() error {
	return nil
}
