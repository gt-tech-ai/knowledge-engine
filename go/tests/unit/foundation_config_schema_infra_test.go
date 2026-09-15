package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/config/schema/infra"
)

// TestCryptoConfig_Validate tests the data-at-rest key config contract.
//
// Why this test is important:
//   - There is deliberately NO hardcoded encryption key; a missing key must fail
//     loudly at startup rather than silently encrypting with an empty key.
//
// What it tests:
//   - The default is empty and rejects; a supplied key validates.
func TestCryptoConfig_Validate(t *testing.T) {
	t.Parallel()

	require.Empty(t, infra.DefaultCryptoConfig().ConnectorCredentialsKey)

	err := infra.DefaultCryptoConfig().Validate()
	require.Error(t, err)
	require.True(
		t,
		errors.Is(err, errors.CodeInvalidInput),
		"missing key is InvalidInput",
	)

	require.NoError(t, infra.CryptoConfig{ConnectorCredentialsKey: "a-key"}.Validate())
}

// TestNotificationsRetentionConfig_Validate tests the bell-inbox retention contract.
//
// Why this test is important:
//   - A non-positive TTL or purge interval would make the retention ticker either
//     drop everything immediately or never run — both silently break the inbox.
//
// What it tests:
//   - The default is a positive 30-day/1-hour window that validates; a non-positive
//     TTL or purge interval each reject as InvalidInput.
func TestNotificationsRetentionConfig_Validate(t *testing.T) {
	t.Parallel()

	def := infra.DefaultNotificationsRetentionConfig()
	require.Equal(t, 30*24*time.Hour, def.TTL)
	require.Equal(t, time.Hour, def.PurgeInterval)
	require.NoError(t, def.Validate())

	require.True(t, errors.Is(
		infra.NotificationsRetentionConfig{TTL: 0, PurgeInterval: time.Hour}.Validate(),
		errors.CodeInvalidInput,
	), "non-positive TTL rejects")
	require.True(t, errors.Is(
		infra.NotificationsRetentionConfig{TTL: time.Hour, PurgeInterval: 0}.Validate(),
		errors.CodeInvalidInput,
	), "non-positive purge interval rejects")
}

// TestPendingNotificationsConfig_Validate tests the durable offline-queue config contract.
//
// Why this test is important:
//   - The per-user bound + lease + TTL keep the pending_notifications table bounded and
//     the at-least-once claim correct; a non-positive value would unbound the table or
//     break re-claim, so each must be rejected before the queue starts.
//
// What it tests:
//   - The default validates; each of MaxPerUser/ClaimLease/TTL/PurgeInterval, when
//     non-positive, rejects as InvalidInput.
func TestPendingNotificationsConfig_Validate(t *testing.T) {
	t.Parallel()

	def := infra.DefaultPendingNotificationsConfig()
	require.Equal(t, 50, def.MaxPerUser)
	require.Equal(t, 60*time.Second, def.ClaimLease)
	require.Equal(t, 7*24*time.Hour, def.TTL)
	require.Equal(t, time.Hour, def.PurgeInterval)
	require.NoError(t, def.Validate())

	valid := infra.DefaultPendingNotificationsConfig
	cases := []struct {
		mutate func(*infra.PendingNotificationsConfig)
		name   string
	}{
		{
			name:   "non-positive max_per_user",
			mutate: func(c *infra.PendingNotificationsConfig) { c.MaxPerUser = 0 },
		},
		{
			name:   "non-positive claim_lease",
			mutate: func(c *infra.PendingNotificationsConfig) { c.ClaimLease = 0 },
		},
		{
			name:   "non-positive ttl",
			mutate: func(c *infra.PendingNotificationsConfig) { c.TTL = 0 },
		},
		{
			name:   "non-positive purge_interval",
			mutate: func(c *infra.PendingNotificationsConfig) { c.PurgeInterval = 0 },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			require.True(t, errors.Is(c.Validate(), errors.CodeInvalidInput))
		})
	}
}
