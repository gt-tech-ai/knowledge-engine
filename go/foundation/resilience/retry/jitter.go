package retry

import (
	"math/rand/v2"
	"time"
)

// EqualJitter applies equal jitter to a backoff duration: half of d plus a uniform
// random share of the other half, so the effective wait falls in [d/2, d]. It
// desynchronizes retries across callers recovering from the same outage (the AWS
// "equal jitter" strategy) while preserving the growth of the underlying backoff, so
// clients backing off from a shared dependency do not retry in lock-step. A
// non-positive duration is returned unchanged.
func EqualJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := d / 2
	//nolint:gosec // backoff desync, not a security decision — a weak PRNG is fine.
	return half + time.Duration(rand.Int64N(int64(half)+1))
}
