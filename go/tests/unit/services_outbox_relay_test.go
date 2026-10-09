package unit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/services/outbox"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
)

// relayNow is the fixed clock every relay test runs at.
var relayNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// relayConfig is a valid relay config for lane "audit" with the given batch shape.
func relayConfig(sendBatch, concurrency, maxAttempts int) outbox.RelayConfig {
	return outbox.RelayConfig{
		Lane: "audit", BatchSize: 100, SendBatch: sendBatch, Concurrency: concurrency,
		MaxAttempts: maxAttempts, BaseBackoff: time.Second, MaxBackoff: 10 * time.Second,
	}
}

// outboxRecords builds n claimed records of lane "audit" with the given attempt count.
func outboxRecords(n, attempts int) []types.OutboxRecord {
	recs := make([]types.OutboxRecord, n)
	for i := range recs {
		recs[i] = types.OutboxRecord{
			ID: uuid.New(), Tenant: "org-1", Lane: "audit", Payload: []byte{byte(i)},
			Attempts: attempts, CreatedAt: relayNow,
		}
	}
	return recs
}

// fixedClock returns a relay option pinning the clock to relayNow.
func fixedClock() outbox.Option { return outbox.WithClock(func() time.Time { return relayNow }) }

// TestRelay_MarksSentOnSuccess tests that every delivered record is marked sent.
//
// Why this test is important:
//   - A delivered row left pending is redelivered forever; the relay's whole job
//     is to move each delivered row to sent exactly once
//
// What it tests:
//   - five claimed records with SendBatch 2 reach the sink as the chunks
//     [0,1], [2,3], [4] (each exactly once), every id is MarkSent exactly once,
//     nothing is retried or parked, Stats is not queried (metrics are off), and
//     RunOnce returns nil
func TestRelay_MarksSentOnSuccess(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	recs := outboxRecords(5, 1)
	ctx := context.Background()

	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(recs, nil).Times(1)
	for _, chunk := range [][]types.OutboxRecord{recs[0:2], recs[2:4], recs[4:5]} {
		sink.EXPECT().Send(gomock.Any(), chunk).Return(make([]error, len(chunk))).Times(1)
	}
	for _, r := range recs {
		store.EXPECT().MarkSent(gomock.Any(), r.ID).Return(nil).Times(1)
	}

	relay := outbox.NewRelay(relayConfig(2, 2, 5), store, sink, nil, nil, nil, fixedClock())

	require.NoError(t, relay.RunOnce(ctx))
}

// TestRelay_PartialBatchFailureRetriesOnlyFailedRows tests per-record outcomes.
//
// Why this test is important:
//   - A batch sink can deliver some entries and reject others; retrying the
//     delivered ones duplicates them, dropping the failed ones loses them
//
// What it tests:
//   - sink results [nil, err, nil] mark records 0 and 2 sent and retry only
//     record 1, with the sink error's text as lastErr
//   - a sink that returns the wrong number of results fails every record of the
//     chunk (all retried), since no outcome can be attributed
func TestRelay_PartialBatchFailureRetriesOnlyFailedRows(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	recs := outboxRecords(3, 1)
	sendErr := apperr.New(apperr.CodeUnavailable, "queue throttled")
	ctx := context.Background()
	noJitter := outbox.WithJitter(func(d time.Duration) time.Duration { return d })

	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{nil, sendErr, nil})
	store.EXPECT().MarkSent(gomock.Any(), recs[0].ID).Return(nil)
	store.EXPECT().MarkSent(gomock.Any(), recs[2].ID).Return(nil)
	store.EXPECT().Retry(gomock.Any(), recs[1].ID, relayNow.Add(2*time.Second), sendErr.Error()).Return(nil)

	relay := outbox.NewRelay(relayConfig(10, 1, 5), store, sink, nil, nil, nil, fixedClock(), noJitter)
	require.NoError(t, relay.RunOnce(ctx))

	short := outboxRecords(2, 1)
	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(short, nil)
	sink.EXPECT().Send(gomock.Any(), short).Return([]error{nil})
	for _, r := range short {
		store.EXPECT().Retry(gomock.Any(), r.ID, relayNow.Add(2*time.Second),
			"INTERNAL: outbox relay: sink returned 1 results for 2 records").Return(nil)
	}

	require.NoError(t, relay.RunOnce(ctx))
}

// TestRelay_BackoffIsExponentialWithJitterAndCapped tests the retry schedule.
//
// Why this test is important:
//   - A failing destination must be backed off exponentially (not hammered),
//     never beyond the cap (or a recovered sink waits hours), and with jitter so
//     a fleet of relays does not retry in lock-step
//
// What it tests:
//   - with Base 1s and Max 10s, failed records at attempts 1, 2, 3, 4 hand the
//     jitter caps of 2s, 4s, 8s and 10s (16s capped) and are retried at now + the
//     jittered value (here half the cap)
//   - with the default full jitter, the next attempt falls in [now, now+cap]
func TestRelay_BackoffIsExponentialWithJitterAndCapped(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	ctx := context.Background()
	sendErr := apperr.New(apperr.CodeUnavailable, "down")

	var recs []types.OutboxRecord
	for attempts := 1; attempts <= 4; attempts++ {
		recs = append(recs, outboxRecords(1, attempts)...)
	}
	var mu sync.Mutex
	var caps []time.Duration
	half := outbox.WithJitter(func(d time.Duration) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		caps = append(caps, d)
		return d / 2
	})

	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{sendErr, sendErr, sendErr, sendErr})
	wantNext := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second}
	for i, r := range recs {
		store.EXPECT().Retry(gomock.Any(), r.ID, relayNow.Add(wantNext[i]), "UNAVAILABLE: down").Return(nil)
	}

	relay := outbox.NewRelay(relayConfig(10, 1, 10), store, sink, nil, nil, nil, fixedClock(), half)
	require.NoError(t, relay.RunOnce(ctx))
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second}, caps)

	one := outboxRecords(1, 3)
	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(one, nil)
	sink.EXPECT().Send(gomock.Any(), one).Return([]error{sendErr})
	inWindow := gomock.Cond(func(x time.Time) bool {
		return !x.Before(relayNow) && !x.After(relayNow.Add(8*time.Second))
	})
	store.EXPECT().Retry(gomock.Any(), one[0].ID, inWindow, "UNAVAILABLE: down").Return(nil)

	defaultJitter := outbox.NewRelay(relayConfig(10, 1, 10), store, sink, nil, nil, nil, fixedClock())
	require.NoError(t, defaultJitter.RunOnce(ctx))
}

// TestRelay_ParksAfterMaxAttempts tests that an exhausted record is parked.
//
// Why this test is important:
//   - A poison record (one the destination always rejects) must leave rotation
//     after MaxAttempts so it neither blocks the lane nor retries forever
//
// What it tests:
//   - with MaxAttempts 3, a failed record on its 3rd attempt is Parked with the
//     error text and never retried, while a failed record on its 2nd attempt is
//     retried, not parked
func TestRelay_ParksAfterMaxAttempts(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	exhausted := outboxRecords(1, 3)[0]
	young := outboxRecords(1, 2)[0]
	recs := []types.OutboxRecord{exhausted, young}
	reject := apperr.New(apperr.CodeInvalidInput, "malformed payload")

	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{reject, reject})
	store.EXPECT().Park(gomock.Any(), exhausted.ID, reject.Error()).Return(nil).Times(1)
	store.EXPECT().Retry(gomock.Any(), young.ID, gomock.Any(), reject.Error()).Return(nil).Times(1)

	relay := outbox.NewRelay(relayConfig(10, 1, 3), store, sink, nil, nil, nil, fixedClock())

	require.NoError(t, relay.RunOnce(context.Background()))
}

// TestRelay_ReportsDepthAndLag tests the lane gauges and outcome counters.
//
// Why this test is important:
//   - Depth and lag are the outbox's health signals: a stuck relay shows as a
//     growing backlog and an aging oldest row long before anyone notices
//     missing messages downstream
//
// What it tests:
//   - Stats{Pending 7, OldestPending now-90s} sets outbox_depth{audit}=7 and
//     outbox_lag_seconds{audit}=90
//   - one sent, one retried and one parked record increment outbox_sent_total,
//     outbox_retried_total and outbox_parked_total{audit} once each
//   - an empty lane (zero OldestPending) reports lag 0
func TestRelay_ReportsDepthAndLag(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	metrics := mocks.NewMockMetrics(ctrl)
	sent, retried, parked := mocks.NewMockCounter(ctrl), mocks.NewMockCounter(ctrl), mocks.NewMockCounter(ctrl)
	depth, lag := mocks.NewMockGauge(ctrl), mocks.NewMockGauge(ctrl)
	ctx := context.Background()

	metrics.EXPECT().Counter("outbox_sent_total", gomock.Any(), "lane").Return(sent)
	metrics.EXPECT().Counter("outbox_retried_total", gomock.Any(), "lane").Return(retried)
	metrics.EXPECT().Counter("outbox_parked_total", gomock.Any(), "lane").Return(parked)
	metrics.EXPECT().Gauge("outbox_depth", gomock.Any(), "lane").Return(depth)
	metrics.EXPECT().Gauge("outbox_lag_seconds", gomock.Any(), "lane").Return(lag)

	ok, retry, park := outboxRecords(1, 1)[0], outboxRecords(1, 1)[0], outboxRecords(1, 2)[0]
	recs := []types.OutboxRecord{ok, retry, park}
	fail := apperr.New(apperr.CodeUnavailable, "down")
	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{nil, fail, fail})
	store.EXPECT().MarkSent(gomock.Any(), ok.ID).Return(nil)
	store.EXPECT().Retry(gomock.Any(), retry.ID, gomock.Any(), fail.Error()).Return(nil)
	store.EXPECT().Park(gomock.Any(), park.ID, fail.Error()).Return(nil)
	sent.EXPECT().Inc("audit").Times(1)
	retried.EXPECT().Inc("audit").Times(1)
	parked.EXPECT().Inc("audit").Times(1)
	store.EXPECT().Stats(gomock.Any(), "audit").
		Return(types.OutboxStats{Pending: 7, Parked: 1, OldestPending: relayNow.Add(-90 * time.Second)}, nil)
	depth.EXPECT().Set(float64(7), "audit")
	lag.EXPECT().Set(float64(90), "audit")

	relay := outbox.NewRelay(relayConfig(10, 1, 2), store, sink, nil, metrics, nil, fixedClock())
	require.NoError(t, relay.RunOnce(ctx))

	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(nil, nil)
	store.EXPECT().Stats(gomock.Any(), "audit").Return(types.OutboxStats{}, nil)
	depth.EXPECT().Set(float64(0), "audit")
	lag.EXPECT().Set(float64(0), "audit")

	require.NoError(t, relay.RunOnce(ctx))
}

// TestRelay_ClaimErrorIsNotRetried tests that a failed claim is surfaced once.
//
// Why this test is important:
//   - Claim is side-effecting (it counts an attempt and leases the rows); a
//     blind retry could burn a record's attempts without ever sending it
//
// What it tests:
//   - a Claim error is called exactly once, nothing is sent or finalized, and
//     RunOnce returns an error carrying the claim's code (UNAVAILABLE)
func TestRelay_ClaimErrorIsNotRetried(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)

	store.EXPECT().Claim(gomock.Any(), "audit", 100).
		Return(nil, apperr.New(apperr.CodeUnavailable, "db down")).Times(1)

	relay := outbox.NewRelay(relayConfig(10, 1, 3), store, sink, nil, nil, nil, fixedClock())
	err := relay.RunOnce(context.Background())

	require.Error(t, err)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
}

// TestRelay_FinalizeErrorIsReturnedWithoutStoppingOtherRows tests finalize failures.
//
// Why this test is important:
//   - One row's MarkSent failing must not strand the rest of the batch; the row
//     itself is safe (its lease expires and it is re-claimed), but the caller
//     must learn of the failure
//
// What it tests:
//   - with MarkSent failing for record 0, record 1 is still MarkSent and RunOnce
//     returns the INTERNAL finalize error
func TestRelay_FinalizeErrorIsReturnedWithoutStoppingOtherRows(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	recs := outboxRecords(2, 1)

	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(recs, nil)
	sink.EXPECT().Send(gomock.Any(), recs).Return([]error{nil, nil})
	store.EXPECT().MarkSent(gomock.Any(), recs[0].ID).Return(apperr.New(apperr.CodeInternal, "tx aborted"))
	store.EXPECT().MarkSent(gomock.Any(), recs[1].ID).Return(nil)

	relay := outbox.NewRelay(relayConfig(10, 1, 3), store, sink, nil, nil, nil, fixedClock())
	err := relay.RunOnce(context.Background())

	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestRelayConfig_Validate tests the relay's config validation.
//
// Why this test is important:
//   - A zero batch, concurrency or attempt budget would make the relay spin
//     doing nothing or park every record on its first failure; such a config
//     must fail at the composition root, not in production
//
// What it tests:
//   - DefaultRelayConfig("audit") is batch 100, send 10, concurrency 4, park on
//     attempt 10, backoff 1s..5m, and validates
//   - a complete config validates; an empty lane, non-positive BatchSize,
//     SendBatch, Concurrency, MaxAttempts or BaseBackoff, and MaxBackoff below
//     BaseBackoff are each INVALID_INPUT
//   - NewRelay panics on an invalid config
func TestRelayConfig_Validate(t *testing.T) {
	t.Parallel()
	require.NoError(t, relayConfig(10, 1, 3).Validate())
	assert.Equal(t, outbox.RelayConfig{
		Lane: "audit", BatchSize: 100, SendBatch: 10, Concurrency: 4, MaxAttempts: 10,
		BaseBackoff: time.Second, MaxBackoff: 5 * time.Minute,
	}, outbox.DefaultRelayConfig("audit"))
	require.NoError(t, outbox.DefaultRelayConfig("audit").Validate())

	broken := map[string]func(*outbox.RelayConfig){
		"lane":        func(c *outbox.RelayConfig) { c.Lane = "" },
		"batch":       func(c *outbox.RelayConfig) { c.BatchSize = 0 },
		"send batch":  func(c *outbox.RelayConfig) { c.SendBatch = 0 },
		"concurrency": func(c *outbox.RelayConfig) { c.Concurrency = 0 },
		"attempts":    func(c *outbox.RelayConfig) { c.MaxAttempts = 0 },
		"base":        func(c *outbox.RelayConfig) { c.BaseBackoff = 0 },
		"max":         func(c *outbox.RelayConfig) { c.MaxBackoff = time.Millisecond },
	}
	for name, mutate := range broken {
		cfg := relayConfig(10, 1, 3)
		mutate(&cfg)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(cfg.Validate()), name)
	}

	ctrl := gomock.NewController(t)
	assert.Panics(t, func() {
		outbox.NewRelay(outbox.RelayConfig{}, mocks.NewMockOutboxStore(ctrl), mocks.NewMockOutboxSink(ctrl),
			nil, nil, nil)
	})
}

// TestRelay_SpansRunsAndLogsStatsFailure tests the relay's tracing and logging.
//
// Why this test is important:
//   - A failing run must be visible on its trace, and a failing Stats query
//     (which only feeds the gauges) must be logged rather than fail the run
//
// What it tests:
//   - a run whose claim fails opens span "outbox.relay.run" with attribute
//     outbox.lane=audit, records the error and sets status Error, then ends it
//   - a run whose Stats fails returns nil, leaves the gauges untouched and logs
//     one Warn "outbox relay: stats failed"
func TestRelay_SpansRunsAndLogsStatsFailure(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sink := mocks.NewMockOutboxSink(ctrl)
	tracer, span := mocks.NewMockTracer(ctrl), mocks.NewMockSpan(ctrl)
	logger := mocks.NewMockLogger(ctrl)
	metrics := mocks.NewMockMetrics(ctrl)
	gauge := mocks.NewMockGauge(ctrl)
	ctx := context.Background()

	metrics.EXPECT().Counter(gomock.Any(), gomock.Any(), "lane").Return(mocks.NewMockCounter(ctrl)).Times(3)
	metrics.EXPECT().Gauge(gomock.Any(), gomock.Any(), "lane").Return(gauge).Times(2)
	claimErr := apperr.New(apperr.CodeUnavailable, "db down")
	tracer.EXPECT().Start(gomock.Any(), "outbox.relay.run").Return(ctx, span).Times(2)
	span.EXPECT().SetAttribute("outbox.lane", "audit").Times(2)
	span.EXPECT().RecordError(gomock.Any()).Times(1)
	span.EXPECT().SetStatus(interfaces.SpanStatusError, gomock.Any()).Times(1)
	span.EXPECT().End().Times(2)
	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(nil, claimErr)

	relay := outbox.NewRelay(relayConfig(10, 1, 3), store, sink, logger, metrics, tracer, fixedClock())
	require.Error(t, relay.RunOnce(ctx))

	store.EXPECT().Claim(gomock.Any(), "audit", 100).Return(nil, nil)
	store.EXPECT().Stats(gomock.Any(), "audit").Return(types.OutboxStats{}, claimErr)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger)
	logger.EXPECT().Warn("outbox relay: stats failed", "lane", "audit", "error", claimErr.Error()).Times(1)

	require.NoError(t, relay.RunOnce(ctx))
}
