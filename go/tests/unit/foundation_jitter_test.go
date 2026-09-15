package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/resilience/retry"
)

// TestEqualJitter_RangeAndVariation tests that retry.EqualJitter keeps a backoff wait
// within [d/2, d] and actually varies across calls, so callers recovering from the
// same outage don't retry in lock-step.
//
// Why this test is important:
//   - Lock-step retries thunder-herd a recovering dependency; equal jitter spreads them
//     out. EqualJitter is a shared resilience primitive (used by the SQS receive loop and
//     available to any backoff caller), so its range and non-determinism must hold.
//
// What it tests:
//   - EqualJitter(d) is always within [d/2, d]; over many calls it produces more than one
//     distinct value (not deterministic); a non-positive backoff is returned unchanged.
func TestEqualJitter_RangeAndVariation(t *testing.T) {
	t.Parallel()

	const d = 800 * time.Millisecond
	seen := make(map[time.Duration]struct{})
	for range 200 {
		j := retry.EqualJitter(d)
		require.GreaterOrEqual(t, j, d/2, "jitter must be >= d/2 (equal jitter floor)")
		require.LessOrEqual(t, j, d, "jitter must be <= d")
		seen[j] = struct{}{}
	}
	require.Greater(t, len(seen), 1, "jitter must vary across calls (no lock-step)")
	require.Zero(t, retry.EqualJitter(0), "a zero backoff is returned unchanged")
	require.Equal(
		t,
		-5*time.Second,
		retry.EqualJitter(-5*time.Second),
		"a negative backoff is returned unchanged",
	)
}
