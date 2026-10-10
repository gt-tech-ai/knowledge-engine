// Package cassandra is the Cassandra-protocol AnalyticsStore (Apache Cassandra or
// Amazon Keyspaces behind clients/cassandra.Session).
//
// Model: one table per declared cube and grain (TableName), partition
// ((org_id, cube, bucket)) with bucket = ts truncated to the grain's bucket width,
// clustering (ts, dims_key, idempotency_key). Each fact writes one partial row
// per grain — partials map<text, blob> holding a vizql.EncodePartial per measure —
// so a redelivered fact overwrites its own row with the same bytes: writes are
// idempotent with no lightweight transaction, no counter and no read-before-write.
// Aggregate reads the buckets of the time range a page at a time with bounded
// prefetch and applies the filter in Go (see residual.go). CQL stays in the
// dialect both backends share.
package cassandra

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/cassandra"
	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/vizql"
)

// Compile-time interface assertions.
var (
	_ interfaces.AnalyticsStore     = (*Store)(nil)
	_ interfaces.AnalyticsCompactor = (*Store)(nil)
)

// identifier is the shape a cube name must have to become part of a table name.
var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)

// Config configures the store.
type Config struct {
	// Cubes declares each cube and the grains it is rolled up at.
	Cubes map[string][]types.Grain
	// BucketWidth is each grain's partition bucket, as a coarser grain.
	BucketWidth map[types.Grain]types.Grain
	// TTL is how long rows of each grain are kept (0 or absent = forever).
	TTL map[types.Grain]time.Duration
	// Keyspace qualifies the table names in SchemaCQL.
	Keyspace string
	// PageSize is the rows per read page (0 = 500).
	PageSize int
	// MaxConcurrentBuckets bounds bucket fan-out on reads and writes (0 = 8).
	MaxConcurrentBuckets int
}

// Store is the Cassandra-protocol AnalyticsStore.
type Store struct {
	// session is the Cassandra-protocol session (its lifecycle is the store's);
	// nil until Start when the store dials lazily.
	session cassandra.Session
	// dial opens the session at Start (nil when New was given a session).
	dial func() (cassandra.Session, error)
	// cfg is the validated configuration.
	cfg Config
	// mu guards session.
	mu sync.RWMutex
}

// New returns a store over session. A cube name that is not a lower-case
// identifier, or a declared grain that is unknown or lacks a known bucket width,
// is CodeInvalidInput.
func New(session cassandra.Session, cfg Config) (*Store, error) {
	s, err := newStore(cfg)
	if err != nil {
		return nil, err
	}
	s.session = session
	return s, nil
}

// NewLazy returns a store that dials its session with dial at Start, so
// construction does no I/O; an operation before Start is CodeUnavailable. The
// config is validated as by New.
func NewLazy(dial func() (cassandra.Session, error), cfg Config) (*Store, error) {
	s, err := newStore(cfg)
	if err != nil {
		return nil, err
	}
	s.dial = dial
	return s, nil
}

// newStore validates cfg and applies its defaults.
func newStore(cfg Config) (*Store, error) {
	if cfg.PageSize <= 0 {
		cfg.PageSize = 500
	}
	if cfg.MaxConcurrentBuckets <= 0 {
		cfg.MaxConcurrentBuckets = 8
	}
	for cube, grains := range cfg.Cubes {
		if !identifier.MatchString(cube) {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"analytics: invalid cube name "+cube,
			)
		}
		for _, g := range grains {
			width, ok := cfg.BucketWidth[g]
			if !ok {
				return nil, apperr.New(
					apperr.CodeInvalidInput,
					"analytics: no bucket width for grain "+string(g),
				)
			}
			if !vizql.ValidGrain(g) || !vizql.ValidGrain(width) {
				return nil, apperr.New(
					apperr.CodeInvalidInput,
					"analytics: unknown grain or bucket width for "+string(g),
				)
			}
		}
	}
	return &Store{cfg: cfg}, nil
}

// TableName returns the table holding cube's rows at grain.
func TableName(cube string, grain types.Grain) string {
	return cube + "_" + string(grain)
}

// SchemaCQL returns the CREATE TABLE statement of cube at grain in keyspace.
func SchemaCQL(keyspace, cube string, grain types.Grain) (string, error) {
	if !identifier.MatchString(cube) || !identifier.MatchString(keyspace) {
		return "", apperr.New(
			apperr.CodeInvalidInput,
			"analytics: invalid keyspace or cube name",
		)
	}
	return "CREATE TABLE IF NOT EXISTS " + keyspace + "." + TableName(cube, grain) + ` (
  org_id text, cube text, bucket timestamp, ts timestamp, dims_key text,
  idempotency_key text,
  dims map<text, text>, partials map<text, blob>,
  PRIMARY KEY ((org_id, cube, bucket), ts, dims_key, idempotency_key))`, nil
}

// Start dials the session of a lazy store (once; a store built by New, or
// already started, does nothing). A dial failure is returned as coded by dial.
func (s *Store) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil || s.dial == nil {
		return nil
	}
	session, err := s.dial()
	if err != nil {
		return err
	}
	s.session = session
	return nil
}

// Stop closes the session, if one was opened.
func (s *Store) Stop(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		s.session.Close()
		s.session = nil
	}
	return nil
}

// sess returns the open session; before Start (or after Stop) it is CodeUnavailable.
func (s *Store) sess() (cassandra.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.session == nil {
		return nil, apperr.New(apperr.CodeUnavailable, "analytics: store is not started")
	}
	return s.session, nil
}

// DimsKey renders dims canonically (sorted, URL-escaped key=value pairs): the
// clustering key that groups rows with identical dimensions.
func DimsKey(dims map[string]string) string {
	values := url.Values{}
	for k, v := range dims {
		values.Set(k, v)
	}
	return values.Encode()
}

// Write upserts one partial row per fact per declared grain of its cube, with
// bounded concurrency. A fact for an undeclared cube, without an org or with an
// empty idempotency key (reserved for compacted rows), with a schema version
// other than FactSchemaVersion (0, an in-process fact, is accepted), or with a
// non-finite measure, is CodeInvalidInput.
func (s *Store) Write(ctx context.Context, facts []types.Fact) error {
	type job struct {
		partials map[string][]byte
		grain    types.Grain
		fact     types.Fact
	}
	session, err := s.sess()
	if err != nil {
		return err
	}
	var jobs []job
	for _, fact := range facts {
		grains, ok := s.cfg.Cubes[fact.Cube]
		if !ok {
			return apperr.New(
				apperr.CodeInvalidInput,
				"analytics: undeclared cube "+fact.Cube,
			)
		}
		if fact.OrgID == "" || fact.IdempotencyKey == "" {
			return apperr.New(
				apperr.CodeInvalidInput,
				"analytics: a fact needs an org_id and an idempotency_key",
			)
		}
		if fact.Schema != 0 && fact.Schema != types.FactSchemaVersion {
			return apperr.New(
				apperr.CodeInvalidInput,
				"analytics: unknown fact schema version",
			)
		}
		partials := make(map[string][]byte, len(fact.Measures))
		for name, v := range fact.Measures {
			p, err := vizql.NewPartial(v)
			if err != nil {
				return err
			}
			partials[name] = vizql.EncodePartial(p)
		}
		for _, grain := range grains {
			jobs = append(jobs, job{partials: partials, fact: fact, grain: grain})
		}
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.cfg.MaxConcurrentBuckets)
	for _, j := range jobs {
		g.Go(func() error { return s.insert(gctx, session, j.fact, j.grain, j.partials) })
	}
	return g.Wait()
}

// insert writes one fact's partial row into grain's table.
func (s *Store) insert(
	ctx context.Context,
	session cassandra.Session,
	fact types.Fact,
	grain types.Grain,
	partials map[string][]byte,
) error {
	stmt := "INSERT INTO " + TableName(fact.Cube, grain) +
		" (org_id, cube, bucket, ts, dims_key, idempotency_key, dims, partials)" +
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
	args := []any{
		fact.OrgID,
		fact.Cube,
		vizql.Truncate(fact.TS, s.cfg.BucketWidth[grain]),
		vizql.Truncate(fact.TS, grain),
		DimsKey(fact.Dims),
		fact.IdempotencyKey,
		fact.Dims,
		partials,
	}
	if ttl := s.cfg.TTL[grain]; ttl > 0 {
		stmt += " USING TTL ?"
		args = append(args, int(ttl/time.Second))
	}
	return session.Query(stmt, args...).WithContext(ctx).Idempotent(true).Exec()
}

// Aggregate opens a page-at-a-time stream over the buckets of q.TimeRange. The
// table is q.Grain's (the cube's coarsest grain when unset). An undeclared cube or
// grain, a missing org, an open-ended time range, or a resume token issued for a
// different query is CodeInvalidInput; a store not yet started is CodeUnavailable.
func (s *Store) Aggregate(
	ctx context.Context,
	q types.AggregateQuery,
) (interfaces.RowStream, error) {
	grains, ok := s.cfg.Cubes[q.Cube]
	if !ok {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: undeclared cube "+q.Cube,
		)
	}
	grain := q.Grain
	if grain == "" && len(grains) > 0 {
		grain = coarsest(grains)
	}
	if !slices.Contains(grains, grain) {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: cube "+q.Cube+" has no grain "+string(grain),
		)
	}
	if q.OrgID == "" || q.TimeRange.From.IsZero() || q.TimeRange.To.IsZero() ||
		!q.TimeRange.From.Before(q.TimeRange.To) {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: a query needs an org_id and a bounded time range",
		)
	}
	width := s.cfg.BucketWidth[grain]
	lo, hi := pushdown(q.Filter, q.TimeRange.From.UTC(), q.TimeRange.To.UTC())
	var buckets []time.Time
	for b := vizql.Truncate(lo, width); b.Before(hi); b = vizql.Next(b, width) {
		buckets = append(buckets, b)
	}
	fingerprint, err := queryFingerprint(q)
	if err != nil {
		return nil, err
	}
	resume, err := decodeToken(q.ResumeToken, fingerprint)
	if err != nil {
		return nil, err
	}
	session, err := s.sess()
	if err != nil {
		return nil, err
	}
	plan := &readPlan{
		session:     session,
		fingerprint: fingerprint,
		table:       TableName(q.Cube, grain),
		org:         q.OrgID,
		cube:        q.Cube,
		lo:          lo,
		hi:          hi,
		pageSize:    s.cfg.PageSize,
		groupBy:     q.GroupBy,
		measures:    measureNames(q.Measures),
		keep:        compileResidual(q.Filter),
	}
	return newStream(ctx, plan, buckets, resume, s.cfg.MaxConcurrentBuckets), nil
}

// coarsest returns the coarsest of grains (month > day > hour > minute).
func coarsest(grains []types.Grain) types.Grain {
	order := []types.Grain{
		types.GrainMonth,
		types.GrainDay,
		types.GrainHour,
		types.GrainMinute,
	}
	for _, g := range order {
		if slices.Contains(grains, g) {
			return g
		}
	}
	return grains[0]
}

// measureNames returns the distinct measure names q computes.
func measureNames(measures []types.MeasureRef) []string {
	var out []string
	for _, m := range measures {
		if !slices.Contains(out, m.Name) {
			out = append(out, m.Name)
		}
	}
	return out
}
