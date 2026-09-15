// Package workers is the shared config surface for the worker tier
// it generalizes the document-events WorkerConfig — the maturity
// reference with the sweep-timing cross-field invariant — so every worker inherits
// the typed-field + Validate shape (gate/jobs/services/clients tunables). Pure
// data; the worker's composition root converts it into its runtime knobs.
package workers

import (
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// Config holds the shared worker tunables. Sweep-timing fields default to zero
// for workers that run no sweeps; the sweep worker (document-events) sets them and
// Validate enforces the cross-field invariant against the multipart presign expiry.
type Config struct {
	// ServiceName is the worker's name for telemetry + structured logging.
	ServiceName string `mapstructure:"service_name"`

	// Env is the deployment environment (dev = verbose logging; staging/prod = top-layer).
	Env string `mapstructure:"env"`

	// OTelEndpoint is the OTLP collector endpoint traces export to.
	OTelEndpoint string `mapstructure:"otel_endpoint"`

	// RelayInterval is how often a poll-and-publish relay runs (0 = no relay).
	RelayInterval time.Duration `mapstructure:"relay_interval"`

	// CleanupInterval is how often an orphaned-object cleanup sweep runs.
	CleanupInterval time.Duration `mapstructure:"cleanup_interval"`

	// ReaperInterval is how often a stale-upload reaper runs.
	ReaperInterval time.Duration `mapstructure:"reaper_interval"`

	// ReaperTTL is the age after which an in-flight upload is reaped; MUST exceed
	// the multipart presign expiry (the cross-field invariant).
	ReaperTTL time.Duration `mapstructure:"reaper_ttl"`

	// RetentionInterval is how often a sent-row retention sweep runs.
	RetentionInterval time.Duration `mapstructure:"retention_interval"`

	// RetentionTTL is how long a published row is kept before purge.
	RetentionTTL time.Duration `mapstructure:"retention_ttl"`

	// OrphanGrace is the grace before an orphaned object is swept; MUST exceed the
	// multipart presign expiry (the cross-field invariant).
	OrphanGrace time.Duration `mapstructure:"orphan_grace"`

	// Port is the worker's health/metrics HTTP port.
	Port int `mapstructure:"port"`

	// BatchSize is a batched worker's per-poll batch size.
	BatchSize int `mapstructure:"batch_size"`

	// FanOutWorkers is the per-batch concurrent fan-out width.
	FanOutWorkers int `mapstructure:"fan_out_workers"`
}

// DefaultConfig returns the shared worker tunables with the same common-knob values
// the document-events worker ships today (its DefaultWorkerConfig common subset), so
// composing this tier over an empty overlay is behavior-preserving. A worker seeds this,
// overlays its `workers` config key, and maps the result onto its runtime config; the
// field-for-field parity is guarded by the worker's default-parity test.
func DefaultConfig() Config {
	return Config{
		ServiceName:       "document-events",
		Env:               "dev",
		OTelEndpoint:      "localhost:4317",
		RelayInterval:     10 * time.Second,
		CleanupInterval:   24 * time.Hour,
		ReaperInterval:    5 * time.Minute,
		ReaperTTL:         90 * time.Minute, // > the 60m multipart part-URL expiry
		RetentionInterval: 1 * time.Hour,
		RetentionTTL:      7 * 24 * time.Hour, // keep a week of sent rows for audit/debug
		OrphanGrace:       2 * time.Hour,      // > the 60m multipart part-URL expiry
		Port:              8085,
		BatchSize:         100,
		FanOutWorkers:     8,
	}
}

// Validate checks the generic worker invariants and, when multipartExpiry > 0 and
// the sweep TTLs are set, enforces the sweep-timing cross-field invariant (the
// reaper/orphan TTLs must outlive an in-flight multipart upload's presign window,
// or a sweep could delete a live upload).
func (c Config) Validate(multipartExpiry time.Duration) error {
	if c.Port < 0 {
		return apperr.InvalidInput("workers.port must be >= 0")
	}
	if c.BatchSize < 0 || c.FanOutWorkers < 0 {
		return apperr.InvalidInput("workers.batch_size / fan_out_workers must be >= 0")
	}
	if multipartExpiry <= 0 {
		return nil
	}
	if c.ReaperTTL > 0 && c.ReaperTTL <= multipartExpiry {
		return apperr.InvalidInput(fmt.Sprintf(
			"workers.reaper_ttl (%s) must exceed the multipart presign expiry (%s)",
			c.ReaperTTL, multipartExpiry,
		))
	}
	if c.OrphanGrace > 0 && c.OrphanGrace <= multipartExpiry {
		return apperr.InvalidInput(fmt.Sprintf(
			"workers.orphan_grace (%s) must exceed the multipart presign expiry (%s)",
			c.OrphanGrace, multipartExpiry,
		))
	}
	return nil
}
