// Package decorators wraps an AnalyticsStore with resilience and observability, in
// the order recovery → metrics → tracing → circuit breaker → timeout (outermost
// first), and wraps a clients/cassandra.Session's writes in the client stack. Apply
// them at the composition root; a nil collaborator or zero timeout skips its layer.
package decorators

import (
	"context"
	"fmt"
	"time"

	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/decorator"
)

// tier labels the store's metrics, spans and logs.
const tier = "analytics"

// Builder composes the decorators around a base AnalyticsStore.
type Builder struct {
	// base is the store being decorated.
	base interfaces.AnalyticsStore
	// logger records recovered panics (nil = not logged).
	logger interfaces.Logger
	// metrics records executions, errors and durations (nil = off).
	metrics interfaces.Metrics
	// tracer opens a span per operation (nil = off).
	tracer interfaces.Tracer
	// cb sheds load while the store is failing (nil = off).
	cb interfaces.CircuitBreaker
	// name labels this store instance.
	name string
	// timeout bounds opening a stream and each write (0 = off).
	timeout time.Duration
}

// NewBuilder starts a decorator chain around base, labelled name.
func NewBuilder(base interfaces.AnalyticsStore, name string) *Builder {
	return &Builder{base: base, name: name}
}

// WithLogger logs recovered panics.
func (b *Builder) WithLogger(l interfaces.Logger) *Builder { b.logger = l; return b }

// WithMetrics records analytics_executions_total, analytics_errors_total and
// analytics_execute_duration_seconds per operation.
func (b *Builder) WithMetrics(m interfaces.Metrics) *Builder { b.metrics = m; return b }

// WithTracer opens a span per operation.
func (b *Builder) WithTracer(t interfaces.Tracer) *Builder { b.tracer = t; return b }

// WithCircuitBreaker sheds operations while the breaker is open (CodeUnavailable).
func (b *Builder) WithCircuitBreaker(cb interfaces.CircuitBreaker) *Builder { b.cb = cb; return b }

// WithTimeout bounds opening a stream and each write; an expired deadline is CodeTimeout.
func (b *Builder) WithTimeout(d time.Duration) *Builder { b.timeout = d; return b }

// Build returns the decorated store.
func (b *Builder) Build() interfaces.AnalyticsStore {
	return &store{
		base: b.base,
		aggregate: chain(b, "aggregate", execFunc[types.AggregateQuery, interfaces.RowStream](
			func(ctx context.Context, q types.AggregateQuery) (interfaces.RowStream, error) {
				return b.base.Aggregate(ctx, q)
			})),
		write: chain(b, "write", execFunc[[]types.Fact, struct{}](
			func(ctx context.Context, facts []types.Fact) (struct{}, error) {
				return struct{}{}, b.base.Write(ctx, facts)
			})),
	}
}

// chain applies the layers to one operation, innermost first.
func chain[In, Out any](b *Builder, op string, e decorator.Executor[In, Out]) decorator.Executor[In, Out] {
	name := b.name + "." + op
	if b.timeout > 0 {
		e = &codedTimeout[In, Out]{inner: decorator.Timeout(e, b.timeout)}
	}
	if b.cb != nil {
		e = &breaker[In, Out]{inner: e, cb: b.cb}
	}
	if b.tracer != nil {
		e = decorator.Tracing(e, b.tracer, tier, name)
	}
	if b.metrics != nil {
		e = decorator.Metrics(e, tier, name, b.metrics, decorator.DefaultBuckets)
	}
	return decorator.Recovery(e, b.logger, tier, name, func(n string, r any) error {
		return coreerr.New(coreerr.CodeInternal, fmt.Sprintf("analytics %s panic recovered: %v", n, r))
	})
}

// store is the decorated AnalyticsStore.
type store struct {
	// base provides the lifecycle.
	base interfaces.AnalyticsStore
	// aggregate is the decorated Aggregate.
	aggregate decorator.Executor[types.AggregateQuery, interfaces.RowStream]
	// write is the decorated Write.
	write decorator.Executor[[]types.Fact, struct{}]
}

// Start starts the base store.
func (s *store) Start(ctx context.Context) error { return s.base.Start(ctx) }

// Stop stops the base store.
func (s *store) Stop(ctx context.Context) error { return s.base.Stop(ctx) }

// Aggregate runs the decorated Aggregate.
func (s *store) Aggregate(ctx context.Context, q types.AggregateQuery) (interfaces.RowStream, error) {
	return s.aggregate.Execute(ctx, q)
}

// Write runs the decorated Write.
func (s *store) Write(ctx context.Context, facts []types.Fact) error {
	_, err := s.write.Execute(ctx, facts)
	return err
}

// Compact forwards to the base store's compactor; a base without one is
// CodeInvalidInput.
func (s *store) Compact(ctx context.Context, cube string, grain types.Grain, org string, bucket time.Time) error {
	c, ok := s.base.(interfaces.AnalyticsCompactor)
	if !ok {
		return coreerr.New(coreerr.CodeInvalidInput, "analytics: store does not compact")
	}
	return c.Compact(ctx, cube, grain, org, bucket)
}

// execFunc adapts a function to decorator.Executor.
type execFunc[In, Out any] func(ctx context.Context, in In) (Out, error)

// Execute calls the function.
func (f execFunc[In, Out]) Execute(ctx context.Context, in In) (Out, error) { return f(ctx, in) }

// codedTimeout turns an expired deadline into CodeTimeout.
type codedTimeout[In, Out any] struct {
	// inner is the deadline-bound executor.
	inner decorator.Executor[In, Out]
}

// Execute runs inner and codes a deadline overrun.
func (d *codedTimeout[In, Out]) Execute(ctx context.Context, in In) (Out, error) {
	out, err := d.inner.Execute(ctx, in)
	if err != nil && coreerr.StdIs(err, context.DeadlineExceeded) && coreerr.Code(err) != coreerr.CodeTimeout {
		return out, coreerr.Wrap(err, coreerr.CodeTimeout, "analytics: operation timed out")
	}
	return out, err
}

// breaker runs each operation through a circuit breaker; a rejection without
// running the operation (an open breaker) is CodeUnavailable.
type breaker[In, Out any] struct {
	// inner is the protected executor.
	inner decorator.Executor[In, Out]
	// cb decides whether to run it.
	cb interfaces.CircuitBreaker
}

// Execute runs inner through the breaker.
func (d *breaker[In, Out]) Execute(ctx context.Context, in In) (Out, error) {
	var out Out
	ran := false
	err := d.cb.Execute(func() error {
		ran = true
		var inner error
		out, inner = d.inner.Execute(ctx, in)
		return inner
	})
	if err != nil && !ran {
		return out, coreerr.Wrap(err, coreerr.CodeUnavailable, "analytics: circuit breaker open")
	}
	return out, err
}
