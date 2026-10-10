package interfaces

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
)

// MetricsQuerier evaluates metric queries (PromQL) against a metrics backend. Both
// calls fail closed: a value that can't be measured is an error, never a silent zero.
type MetricsQuerier interface {
	// Query evaluates query as an instant query and returns its single sample value.
	Query(ctx context.Context, query string) (float64, error)

	// QueryRange evaluates query over [start, end] at step and returns its single
	// series; no series is an empty series.
	QueryRange(
		ctx context.Context,
		query string,
		start, end time.Time,
		step time.Duration,
	) ([]types.MetricSample, error)
}
