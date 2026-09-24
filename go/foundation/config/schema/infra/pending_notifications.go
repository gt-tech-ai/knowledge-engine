package infra

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// PendingNotificationsConfig tunes the durable offline-notification queue: the per-user
// bound + claim lease on the Postgres-backed pending_notifications table, and the API-owned retention
// ticker that drops rows past their TTL. Persistence is Postgres in every environment (no kind) — these
// are only the bound + lifetimes; the API service owns the table, so cleanup is its ticker, not a job.
type PendingNotificationsConfig struct {
	// MaxPerUser bounds a user's queued rows; Enqueue evicts the oldest past this cap so the table
	// stays bounded regardless of how long a user stays offline.
	MaxPerUser int `mapstructure:"max_per_user"`

	// ClaimLease is how long a claimed-but-unconfirmed row is hidden from re-claim (the at-least-once
	// lease window): a crash between the reconnect claim and the delivery mark re-claims after this.
	ClaimLease time.Duration `mapstructure:"claim_lease"`

	// TTL is how long an undelivered row is retained before the purge ticker drops it; a user offline
	// longer than this refetches on next login.
	TTL time.Duration `mapstructure:"ttl"`

	// PurgeInterval is how often the API-owned retention ticker runs a purge pass.
	PurgeInterval time.Duration `mapstructure:"purge_interval"`
}

// DefaultPendingNotificationsConfig returns a 50-row-per-user cap, a 60s claim lease, a 7-day TTL, and
// an hourly purge, matching the base config. An absent config section keeps these.
func DefaultPendingNotificationsConfig() PendingNotificationsConfig {
	return PendingNotificationsConfig{
		MaxPerUser:    50,
		ClaimLease:    60 * time.Second,
		TTL:           7 * 24 * time.Hour,
		PurgeInterval: time.Hour,
	}
}

// Validate rejects a non-positive bound, lease, TTL, or purge interval.
func (c PendingNotificationsConfig) Validate() error {
	if c.MaxPerUser <= 0 {
		return apperr.InvalidInput("pending_notifications.max_per_user must be positive")
	}
	if c.ClaimLease <= 0 {
		return apperr.InvalidInput("pending_notifications.claim_lease must be positive")
	}
	if c.TTL <= 0 {
		return apperr.InvalidInput("pending_notifications.ttl must be positive")
	}
	if c.PurgeInterval <= 0 {
		return apperr.InvalidInput(
			"pending_notifications.purge_interval must be positive",
		)
	}
	return nil
}
