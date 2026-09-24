package interceptors

import (
	"context"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/interceptorcore"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/resilience/budget"
)

// DefaultRetryBudget is the per-request retry cap servers attach at the
// entrypoint: a single logical request may spend at most this many retries
// across every retrier in its chain (DB repo retriers + outbound client
// retriers), so one request can never fan out into unbounded retries.
const DefaultRetryBudget int32 = 10

// nonRetryableConnectCodes lists Connect error codes that must not be retried:
// permanent client/domain failures, plus ResourceExhausted — retrying a
// rate-limited/quota-exceeded call immediately only adds load and deepens the
// backpressure. A parent-context deadline/cancel is handled by the retrier's
// context-awareness (it stops when ctx is done), not classified here, so a
// per-attempt (child-context) timeout can still be retried.
var nonRetryableConnectCodes = map[connect.Code]bool{
	connect.CodeInvalidArgument:   true,
	connect.CodeNotFound:          true,
	connect.CodeAlreadyExists:     true,
	connect.CodePermissionDenied:  true,
	connect.CodeUnauthenticated:   true,
	connect.CodeUnimplemented:     true,
	connect.CodeCanceled:          true,
	connect.CodeResourceExhausted: true,
}

// isNonRetryableConnect reports whether err carries a Connect error code that
// must not be retried — the framework-specific classifier the interceptorcore
// retry loop calls.
func isNonRetryableConnect(err error) bool {
	return nonRetryableConnectCodes[connect.CodeOf(err)]
}

// RetryInterceptor returns a client-side interceptor that retries transient
// failures using the provided Retrier. Non-retryable error codes (e.g.
// InvalidArgument, NotFound) are returned immediately without retry.
func RetryInterceptor(retrier interfaces.Retrier) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			var finalResp connect.AnyResponse

			permanent, exhausted := interceptorcore.Retry(
				ctx,
				retrier,
				isNonRetryableConnect,
				func() error {
					resp, err := next(ctx, req)
					if err != nil {
						return err
					}
					finalResp = resp
					return nil
				},
			)

			if permanent != nil {
				return nil, permanent
			}
			if exhausted != nil {
				return nil, connect.NewError(connect.CodeUnavailable, exhausted)
			}
			return finalResp, nil
		}
	}
}

// BudgetInterceptor attaches a fresh retry budget to each inbound request's
// context at the server entrypoint. Every retrier in the downstream chain —
// the handler's repo/DB retriers and any outbound client retriers — then draws
// from this one shared per-request cap instead of each multiplying retries
// independently. maxRetries <= 0 disables it (retries are then bounded only
// per-call by each retrier's own MaxRetries).
func BudgetInterceptor(maxRetries int32) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if maxRetries > 0 {
				ctx = budget.WithRetryBudget(ctx, budget.NewBudget(maxRetries))
			}
			return next(ctx, req)
		}
	}
}
