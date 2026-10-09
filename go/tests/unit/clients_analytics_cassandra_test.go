package unit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	analyticscassandra "github.com/gt-tech-ai/knowledge-engine/go/clients/analytics/cassandra"
	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/listquery"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/vizql"
	"github.com/gt-tech-ai/knowledge-engine/go/tests/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// storedRow is one row a mocked Cassandra page returns.
type storedRow struct {
	ts       time.Time
	dims     map[string]string
	partials map[string][]byte
}

// day returns 2026-<month>-<d> 00:00 UTC.
func day(month time.Month, d int) time.Time { return time.Date(2026, month, d, 0, 0, 0, 0, time.UTC) }

// newCassandraStore builds the store over a mock session with the genai_calls cube
// rolled up hourly and daily.
func newCassandraStore(t *testing.T, session *mocks.MockSession) *analyticscassandra.Store {
	t.Helper()
	store, err := analyticscassandra.New(session, analyticscassandra.Config{
		Cubes: map[string][]types.Grain{"genai_calls": {types.GrainHour, types.GrainDay}},
		BucketWidth: map[types.Grain]types.Grain{
			types.GrainHour: types.GrainDay, types.GrainDay: types.GrainMonth,
		},
		PageSize:             100,
		MaxConcurrentBuckets: 2,
	})
	require.NoError(t, err)
	return store
}

// expectPage expects one paged SELECT of bucket in [lo, hi) and serves rows as a
// single, final page.
func expectPage(ctrl *gomock.Controller, session *mocks.MockSession, bucket, lo, hi time.Time, rows []storedRow) {
	query := mocks.NewMockQuery(ctrl)
	iter := mocks.NewMockIter(ctrl)
	session.EXPECT().Query(gomock.Any(), "org-1", "genai_calls", bucket, lo, hi).Return(query)
	query.EXPECT().WithContext(gomock.Any()).Return(query)
	query.EXPECT().PageSize(100).Return(query)
	query.EXPECT().PageState(gomock.Nil()).Return(query)
	query.EXPECT().Idempotent(true).Return(query)
	query.EXPECT().Iter().Return(iter)
	i := 0
	iter.EXPECT().Scan(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(dest ...any) bool {
		if i >= len(rows) {
			return false
		}
		*dest[0].(*time.Time) = rows[i].ts
		*dest[1].(*map[string]string) = rows[i].dims
		*dest[2].(*map[string][]byte) = rows[i].partials
		i++
		return true
	}).Times(len(rows) + 1)
	iter.EXPECT().PageState().Return(nil)
	iter.EXPECT().Close().Return(nil)
}

// encoded returns the encoded single-observation partial of v.
func encoded(t *testing.T, v float64) []byte {
	t.Helper()
	p, err := vizql.NewPartial(v)
	require.NoError(t, err)
	return vizql.EncodePartial(p)
}

// TestCassandraStore_ReadsEveryBucketOfTheRangeInOrder tests bucket enumeration:
// a day-grain query reads every monthly bucket its range touches, in order.
//
// Why this test is important:
//   - A missed bucket silently drops a month of data; out-of-order delivery breaks
//     the resume token's position
//
// What it tests:
//   - 2026-09-15 → 2026-11-02 reads buckets Sep 1, Oct 1 and Nov 1 (each once)
//   - pages arrive in bucket order, "more" is true until the last bucket, then the
//     stream ends with no rows
//   - rows are projected to the group-by dimensions and requested partials
func TestCassandraStore_ReadsEveryBucketOfTheRangeInOrder(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockSession(ctrl)
	store := newCassandraStore(t, session)
	from, to := day(time.September, 15), day(time.November, 2)
	for _, b := range []time.Time{day(time.September, 1), day(time.October, 1), day(time.November, 1)} {
		expectPage(ctrl, session, b, from, to, []storedRow{{
			ts:       b.AddDate(0, 0, 1),
			dims:     map[string]string{"team": b.Month().String(), "model": "m"},
			partials: map[string][]byte{"tokens_in": encoded(t, 1), "duration_s": encoded(t, 2)},
		}})
	}

	stream, err := store.Aggregate(context.Background(), types.AggregateQuery{
		Cube: "genai_calls", OrgID: "org-1", Grain: types.GrainDay, GroupBy: []string{"team"},
		Measures:  []types.MeasureRef{{Name: "tokens_in", Agg: types.AggSum}},
		TimeRange: types.TimeRange{From: from, To: to},
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, stream.Close()) }()

	var teams []string
	var mores []bool
	for {
		rows, more, err := stream.Next(context.Background())
		require.NoError(t, err)
		for _, r := range rows {
			teams = append(teams, r.Group["team"])
			assert.Equal(t, map[string]string{"team": r.Group["team"]}, r.Group)
			assert.Len(t, r.Partials, 1)
		}
		mores = append(mores, more)
		if !more {
			break
		}
	}
	assert.Equal(t, []string{"September", "October", "November"}, teams)
	assert.Equal(t, []bool{true, true, false}, mores)
	assert.Empty(t, stream.ResumeToken())
}

// TestCassandraStore_PushdownNarrowsRangeAndFiltersResidual tests the filter
// split: time clauses narrow the CQL clustering range, every clause is applied
// in Go.
//
// Why this test is important:
//   - Pushdown cuts what is read; the residual keeps the answer exact — dimension
//     filters cannot run in CQL without ALLOW FILTERING
//
// What it tests:
//   - ts >= Oct 5 narrows [Oct 1, Nov 1) to [Oct 5, Nov 1) and reads only October
//   - team = t-1 keeps the t-1 row and drops the t-2 row
func TestCassandraStore_PushdownNarrowsRangeAndFiltersResidual(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockSession(ctrl)
	store := newCassandraStore(t, session)
	m := listquery.NewMap().Add(listquery.Time("ts").AsTime(), listquery.String("team"))
	filter, err := listquery.Parse(m, `{"$and":[{"$gte":{"ts":"2026-10-05T00:00:00Z"}},{"$eq":{"team":"t-1"}}]}`)
	require.NoError(t, err)
	expectPage(ctrl, session, day(time.October, 1), day(time.October, 5), day(time.November, 1), []storedRow{
		{ts: day(time.October, 6), dims: map[string]string{"team": "t-1"}, partials: map[string][]byte{"tokens_in": encoded(t, 5)}},
		{ts: day(time.October, 7), dims: map[string]string{"team": "t-2"}, partials: map[string][]byte{"tokens_in": encoded(t, 9)}},
	})

	stream, err := store.Aggregate(context.Background(), types.AggregateQuery{
		Cube: "genai_calls", OrgID: "org-1", Grain: types.GrainDay, GroupBy: []string{"team"}, Filter: filter,
		Measures:  []types.MeasureRef{{Name: "tokens_in", Agg: types.AggSum}},
		TimeRange: types.TimeRange{From: day(time.October, 1), To: day(time.November, 1)},
	})
	require.NoError(t, err)
	rows, more, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	assert.False(t, more)
	require.Len(t, rows, 1)
	assert.Equal(t, map[string]string{"team": "t-1"}, rows[0].Group)
	assert.Equal(t, day(time.October, 6), rows[0].TS)
}

// TestCassandraStore_WriteUpsertsOnePartialRowPerGrain tests the idempotent write
// path: one INSERT per declared grain, keyed by the fact's idempotency key.
//
// Why this test is important:
//   - Without per-grain rows a coarse query would have to scan the finest table;
//     without the idempotency key a redelivered fact would double-count
//
// What it tests:
//   - a fact at 2026-10-09 12:34:56 writes genai_calls_hour (bucket Oct 9, ts 12:00)
//     and genai_calls_day (bucket Oct 1, ts Oct 9), both idempotent, with dims_key
//     "model=m&team=t-1" and a tokens_in partial that finalizes to 42
func TestCassandraStore_WriteUpsertsOnePartialRowPerGrain(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	session := mocks.NewMockSession(ctrl)
	store := newCassandraStore(t, session)
	at := time.Date(2026, 10, 9, 12, 34, 56, 0, time.UTC)
	dims := map[string]string{"team": "t-1", "model": "m"}
	insert := "INSERT INTO %s (org_id, cube, bucket, ts, dims_key, idempotency_key, dims, partials) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
	for table, keys := range map[string][2]time.Time{
		"genai_calls_hour": {day(time.October, 9), time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)},
		"genai_calls_day":  {day(time.October, 1), day(time.October, 9)},
	} {
		query := mocks.NewMockQuery(ctrl)
		session.EXPECT().Query(fmt.Sprintf(insert, table), "org-1", "genai_calls", keys[0], keys[1], "model=m&team=t-1", "k-1", dims, gomock.Any()).
			DoAndReturn(func(_ string, values ...any) *mocks.MockQuery {
				p, err := vizql.DecodePartial(values[7].(map[string][]byte)["tokens_in"])
				require.NoError(t, err)
				assert.InDelta(t, 42.0, vizql.Finalize(p, types.AggSum), 0)
				return query
			})
		query.EXPECT().WithContext(gomock.Any()).Return(query)
		query.EXPECT().Idempotent(true).Return(query)
		query.EXPECT().Exec().Return(nil)
	}

	err := store.Write(context.Background(), []types.Fact{{
		Cube: "genai_calls", OrgID: "org-1", TS: at, Dims: dims, Measures: map[string]float64{"tokens_in": 42}, IdempotencyKey: "k-1",
	}})
	require.NoError(t, err)
}

// TestCassandraStore_RejectsInvalidQueriesAndFacts tests the store's coded rejections.
//
// Why this test is important:
//   - An unbounded or org-less read would scan every tenant's partitions; an
//     undeclared cube has no table
//
// What it tests:
//   - Aggregate with an undeclared cube, an undeclared grain, no org, or an open
//     range, and Write with an undeclared cube, no org, or an empty idempotency
//     key, each return CodeInvalidInput (no session call is made)
func TestCassandraStore_RejectsInvalidQueriesAndFacts(t *testing.T) {
	t.Parallel()

	store := newCassandraStore(t, mocks.NewMockSession(gomock.NewController(t)))
	ctx := context.Background()
	rng := types.TimeRange{From: day(time.October, 1), To: day(time.October, 2)}
	for name, q := range map[string]types.AggregateQuery{
		"cube":  {Cube: "other", OrgID: "o", TimeRange: rng},
		"grain": {Cube: "genai_calls", OrgID: "o", Grain: types.GrainMinute, TimeRange: rng},
		"org":   {Cube: "genai_calls", TimeRange: rng},
		"range": {Cube: "genai_calls", OrgID: "o", TimeRange: types.TimeRange{From: rng.From}},
	} {
		_, err := store.Aggregate(ctx, q)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}
	for name, f := range map[string]types.Fact{
		"cube": {Cube: "other", OrgID: "o", IdempotencyKey: "k"},
		"org":  {Cube: "genai_calls", IdempotencyKey: "k"},
		"key":  {Cube: "genai_calls", OrgID: "o"},
	} {
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(store.Write(ctx, []types.Fact{f})), name)
	}
}
