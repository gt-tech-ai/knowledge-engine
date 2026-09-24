package e2ekit

import (
	"context"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// PollUntil calls check every 2s until it returns (true, nil) or the deadline (~timeout) elapses,
// returning the last error/miss. It is the effect analogue of PollTraceInTempo: an async worker's
// "endpoint" is a downstream state change, so a case polls for that state.
func PollUntil(
	ctx context.Context,
	timeout time.Duration,
	what string,
	check func(context.Context) (bool, error),
) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ok, err := check(ctx)
		if ok {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return apperr.Wrap(lastErr, apperr.CodeUnavailable, "e2ekit: "+what)
			}
			return apperr.New(
				apperr.CodeUnavailable,
				"e2ekit: "+what+" did not materialize within "+timeout.String(),
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
