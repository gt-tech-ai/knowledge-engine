package infra

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// NotificationsRetentionConfig tunes retention of the API-owned notification-history read-model — the
// bell inbox. Persistence is Postgres in every environment (no kind); the API service owns
// the notifications table, so cleanup is its own retention ticker (mirroring the pending-queue purger),
// not a cross-service worker job. Growth is bounded by TTL alone: the read list is already capped
// server-side, so there is no per-user row cap here (deferred — YAGNI). This block binds
// notifications.retention.
type NotificationsRetentionConfig struct {
	// TTL is how long a recorded notification is retained before the purge ticker drops it; the bell
	// shows only the most-recent window, so history older than this is not surfaced.
	TTL time.Duration `mapstructure:"ttl"`

	// PurgeInterval is how often the API-owned retention ticker runs a purge pass.
	PurgeInterval time.Duration `mapstructure:"purge_interval"`
}

// DefaultNotificationsRetentionConfig returns a 30-day retention window and an hourly purge, matching
// the base config. An absent config section keeps these.
func DefaultNotificationsRetentionConfig() NotificationsRetentionConfig {
	return NotificationsRetentionConfig{
		TTL:           30 * 24 * time.Hour,
		PurgeInterval: time.Hour,
	}
}

// Validate rejects a non-positive TTL or purge interval.
func (c NotificationsRetentionConfig) Validate() error {
	if c.TTL <= 0 {
		return apperr.InvalidInput("notifications.retention.ttl must be positive")
	}
	if c.PurgeInterval <= 0 {
		return apperr.InvalidInput(
			"notifications.retention.purge_interval must be positive",
		)
	}
	return nil
}
