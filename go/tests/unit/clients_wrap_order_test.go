package unit_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.uber.org/mock/gomock"

	decorators "github.com/gt-tech-ai/knowledge-engine/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// obsOrder records the observability call sequence ("trace" / "metric" / "log") in
// the order the client stack invokes each collaborator, so a test can assert the
// composed §6.3 order. It is plain test data — it implements no production
// interface; the generated metrics/logger mocks and the orderTracer below append to
// it as the stack drives them.
type obsOrder struct {
	tags []string
	mu   sync.Mutex
}

func (o *obsOrder) add(tag string) {
	o.mu.Lock()
	o.tags = append(o.tags, tag)
	o.mu.Unlock()
}

func (o *obsOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.tags...)
}

// orderTracer is the one irreducible test double in this package: go.uber.org/mock
// CANNOT generate a usable mock for go.opentelemetry.io/otel/trace.Tracer, because
// that interface embeds embedded.Tracer, whose marker method tracer() is unexported
// to package embedded — a mock generated in package mocks cannot satisfy it (a
// compile check confirms `*mocks.MockTracer does not implement trace.Tracer
// (unexported method tracer)`). So the OTel tracer seam stays hand-rolled; it embeds
// embedded.Tracer to satisfy that guard and records the span start into the shared
// order log, returning the context's no-op span. Every other collaborator in this
// test is a generated mock.
type orderTracer struct {
	embedded.Tracer
	order *obsOrder
}

// Start records the span start and returns the context's (no-op) span.
func (t orderTracer) Start(
	ctx context.Context,
	_ string,
	_ ...oteltrace.SpanStartOption,
) (context.Context, oteltrace.Span) {
	t.order.add("trace")
	return ctx, oteltrace.SpanFromContext(ctx)
}

// TestClientStack_WrapOrder_TracingMetricsLogging locks the charter §6.3 observability
// order for the client stack: Tracing (outermost) → Metrics → Logging (innermost).
//
// Why this test is important:
//   - The §6.3 order is semantic: the tracing span must bracket the whole operation, and
//
// the failure log is the innermost seam. Before the repo/service builders
//
//	had this inverted (Logging outermost); this test prevents a regression back to that.
//
// What it tests:
//   - On a failing op the recorded observability sequence starts with the tracing span,
//     records metrics next, and logs last — every metric strictly before the log.
func TestClientStack_WrapOrder_TracingMetricsLogging(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	order := &obsOrder{}

	// Metrics: the stack builds two counters (ops, errs) and a duration histogram at
	// WithMetrics time, then records ops.Inc, dur.Observe, errs.Inc on a failing op.
	// Each records a "metric" tag so the order log captures the metrics phase.
	ops := mocks.NewMockCounter(ctrl)
	errs := mocks.NewMockCounter(ctrl)
	dur := mocks.NewMockHistogram(ctrl)
	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().
		Counter("client_operations_total", gomock.Any(), gomock.Any()).
		Return(ops).AnyTimes()
	metrics.EXPECT().
		Counter("client_errors_total", gomock.Any(), gomock.Any()).
		Return(errs).AnyTimes()
	metrics.EXPECT().
		Histogram("client_operation_duration_seconds", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(dur).AnyTimes()
	ops.EXPECT().Inc(gomock.Any()).Do(func(...string) { order.add("metric") }).AnyTimes()
	errs.EXPECT().Inc(gomock.Any()).Do(func(...string) { order.add("metric") }).AnyTimes()
	dur.EXPECT().
		Observe(gomock.Any(), gomock.Any()).
		Do(func(float64, ...string) { order.add("metric") }).AnyTimes()

	// Logger: the client stack is an INNER seam, so a failed op logs at Debug. Record
	// a "log" tag from Debug (and Error, defensively) so the log phase is captured.
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().
		Debug(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { order.add("log") }).AnyTimes()
	logger.EXPECT().
		Error(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { order.add("log") }).AnyTimes()

	tracer := orderTracer{order: order}
	s := decorators.New("test").WithTracer(tracer).WithMetrics(metrics).WithLogger(logger)

	_, _ = decorators.Run(
		context.Background(),
		s,
		"op",
		decorators.RunOpts{},
		func(context.Context) (string, error) { return "", errBoom },
	) // fail so logging fires

	tags := order.snapshot()
	require.NotEmpty(t, tags, "the stack must record trace/metric/log tags")
	require.Equal(
		t,
		"trace",
		tags[0],
		"tracing span must start first (Tracing outermost)",
	)
	require.Equal(t, "log", tags[len(tags)-1], "logging must be last (Logging innermost)")

	logIdx := len(tags) - 1
	for i, tag := range tags {
		if tag == "metric" {
			require.Less(
				t,
				i,
				logIdx,
				"metrics must be recorded before the log (Metrics outside Logging)",
			)
		}
	}
}
