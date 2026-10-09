package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/analytics/decorators"
	clientdecorators "github.com/gt-tech-ai/knowledge-engine/go/clients/decorators"
	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestDecorators_TimeoutReturnsCodedTimeout tests that an analytics operation
// exceeding the decorator's deadline fails with CodeTimeout.
//
// Why this test is important:
//   - A hung Cassandra read must free the request with a retryable, coded error,
//     not an uncoded context error the transport reports as INTERNAL
//
// What it tests:
//   - a Write that blocks until its context ends returns CodeTimeout after the
//     20 ms deadline, and the store saw a cancelled context
func TestDecorators_TimeoutReturnsCodedTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	base := mocks.NewMockAnalyticsStore(ctrl)
	base.EXPECT().Write(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ []types.Fact) error {
		<-ctx.Done()
		return ctx.Err()
	})
	store := decorators.NewBuilder(base, "facts").WithTimeout(20 * time.Millisecond).Build()

	err := store.Write(context.Background(), []types.Fact{{Cube: "c"}})

	assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))
}

// TestDecorators_OpenBreakerReturnsUnavailable tests that an open circuit breaker
// sheds the operation with CodeUnavailable and never calls the store.
//
// Why this test is important:
//   - While the store is down, every caller must fail fast with a retryable code
//     instead of piling onto the failing cluster
//
// What it tests:
//   - with the breaker rejecting without running the operation, Aggregate returns
//     CodeUnavailable and the base store is never invoked
//   - a store error passed through a closed breaker keeps its own code
//   - a panicking store is recovered into CodeInternal
func TestDecorators_OpenBreakerReturnsUnavailable(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	base := mocks.NewMockAnalyticsStore(ctrl)
	open := mocks.NewMockCircuitBreaker(ctrl)
	open.EXPECT().Execute(gomock.Any()).Return(apperr.Sentinel("circuit breaker is open"))
	store := decorators.NewBuilder(base, "facts").WithCircuitBreaker(open).Build()

	_, err := store.Aggregate(context.Background(), types.AggregateQuery{})
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))

	closed := mocks.NewMockCircuitBreaker(ctrl)
	closed.EXPECT().Execute(gomock.Any()).DoAndReturn(func(fn func() error) error { return fn() }).Times(2)
	base.EXPECT().Write(gomock.Any(), gomock.Any()).Return(apperr.New(apperr.CodeInvalidInput, "bad fact"))
	base.EXPECT().Write(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, []types.Fact) error { panic("boom") })
	passing := decorators.NewBuilder(base, "facts").WithCircuitBreaker(closed).Build()
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(passing.Write(context.Background(), nil)))
	assert.Equal(t, apperr.CodeInternal, apperr.Code(passing.Write(context.Background(), nil)))
}

// TestDecorators_SessionRetriesOnlyIdempotentStatements tests the Cassandra
// session decorator's client stack: idempotent statements are retried, others not.
//
// Why this test is important:
//   - Retrying a non-idempotent statement can apply it twice; not retrying an
//     idempotent upsert turns a transient blip into a lost fact
//
// What it tests:
//   - an idempotent Exec failing once with CodeUnavailable is retried and succeeds
//     (two inner Exec calls); a non-idempotent Exec is attempted exactly once
func TestDecorators_SessionRetriesOnlyIdempotentStatements(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	inner := mocks.NewMockSession(ctrl)
	retrier := mocks.NewMockRetrier(ctrl)
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, fn func() error) error {
		if err := fn(); err != nil {
			return fn()
		}
		return nil
	})
	stack := clientdecorators.New("cassandra").WithRetrier(retrier)
	session := decorators.Session(inner, stack)

	upsert := mocks.NewMockQuery(ctrl)
	inner.EXPECT().Query("UPSERT").Return(upsert)
	upsert.EXPECT().WithContext(gomock.Any()).Return(upsert).AnyTimes()
	upsert.EXPECT().Idempotent(true).Return(upsert)
	gomock.InOrder(
		upsert.EXPECT().Exec().Return(apperr.New(apperr.CodeUnavailable, "replica down")),
		upsert.EXPECT().Exec().Return(nil),
	)
	require.NoError(t, session.Query("UPSERT").WithContext(context.Background()).Idempotent(true).Exec())

	once := mocks.NewMockQuery(ctrl)
	inner.EXPECT().Query("ONCE").Return(once)
	once.EXPECT().WithContext(gomock.Any()).Return(once).AnyTimes()
	once.EXPECT().Exec().Return(apperr.New(apperr.CodeUnavailable, "replica down"))
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(session.Query("ONCE").Exec()))
}
