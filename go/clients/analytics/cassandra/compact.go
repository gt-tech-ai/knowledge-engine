package cassandra

import (
	"context"
	"slices"
	"time"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/cassandra"
	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/vizql"
)

// compactChunk is how many per-fact rows one compaction batch folds in: with the
// compacted row's upsert, a batch stays at 30 statements (the Keyspaces limit).
const compactChunk = 29

// groupRows are one (ts, dims_key) group of a partition.
type groupRows struct {
	// ts is the group's grain-aligned time.
	ts time.Time
	// compacted is the merged row's partials (nil before the first compaction).
	compacted map[string][]byte
	// dims are the group's dimension values.
	dims map[string]string
	// dimsKey is the group's stored clustering key.
	dimsKey string
	// keys are the per-fact rows' idempotency keys.
	keys []string
	// facts are the per-fact rows' partials, aligned with keys.
	facts []map[string][]byte
}

// Compact merges a closed bucket's per-fact rows into one row per (ts, dims_key)
// with an empty idempotency key. Each batch upserts the merged row and deletes the
// per-fact rows it absorbed, inside one partition, so it applies atomically and a
// half-done compaction never changes an answer; rerunning it is a no-op. Run one
// compactor per bucket, only for buckets past the facts' redelivery horizon (a
// fact redelivered after compaction would be counted again). An undeclared cube or
// grain, a misaligned bucket, or a bucket that has not ended is CodeInvalidInput.
func (s *Store) Compact(ctx context.Context, cube string, grain types.Grain, org string, bucket time.Time) error {
	grains, ok := s.cfg.Cubes[cube]
	if !ok || !slices.Contains(grains, grain) {
		return errors.New(errors.CodeInvalidInput, "analytics: undeclared cube or grain")
	}
	width := s.cfg.BucketWidth[grain]
	bucket = bucket.UTC()
	if !vizql.Truncate(bucket, width).Equal(bucket) || vizql.Next(bucket, width).After(time.Now()) {
		return errors.New(errors.CodeInvalidInput, "analytics: only a closed, aligned bucket can be compacted")
	}
	groups, err := s.readPartition(ctx, cube, grain, org, bucket)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if err := s.compactGroup(ctx, cube, grain, org, bucket, g); err != nil {
			return err
		}
	}
	return nil
}

// readPartition reads every row of one partition, grouped by (ts, dims_key) in
// clustering order.
func (s *Store) readPartition(ctx context.Context, cube string, grain types.Grain, org string, bucket time.Time) ([]*groupRows, error) {
	var groups []*groupRows
	index := map[string]*groupRows{}
	var state []byte
	for {
		iter := s.session.Query(
			"SELECT ts, dims_key, idempotency_key, dims, partials FROM "+TableName(cube, grain)+
				" WHERE org_id = ? AND cube = ? AND bucket = ?", org, cube, bucket,
		).WithContext(ctx).PageSize(s.cfg.PageSize).PageState(state).Idempotent(true).Iter()
		var ts time.Time
		var dimsKey, key string
		var dims map[string]string
		var partials map[string][]byte
		for iter.Scan(&ts, &dimsKey, &key, &dims, &partials) {
			id := ts.UTC().Format(time.RFC3339Nano) + "|" + dimsKey
			g, ok := index[id]
			if !ok {
				g = &groupRows{ts: ts.UTC(), dims: dims, dimsKey: dimsKey}
				index[id] = g
				groups = append(groups, g)
			}
			if key == "" {
				g.compacted = partials
			} else {
				g.keys = append(g.keys, key)
				g.facts = append(g.facts, partials)
			}
			ts, dimsKey, key, dims, partials = time.Time{}, "", "", nil, nil
		}
		state = iter.PageState()
		if err := iter.Close(); err != nil {
			return nil, err
		}
		if len(state) == 0 {
			return groups, nil
		}
	}
}

// compactGroup folds a group's per-fact rows into its compacted row, a chunk per batch.
func (s *Store) compactGroup(ctx context.Context, cube string, grain types.Grain, org string, bucket time.Time, g *groupRows) error {
	table := TableName(cube, grain)
	dimsKey := g.dimsKey
	upsert := "INSERT INTO " + table +
		" (org_id, cube, bucket, ts, dims_key, idempotency_key, dims, partials) VALUES (?, ?, ?, ?, ?, '', ?, ?)"
	var ttlArgs []any
	if ttl := s.cfg.TTL[grain]; ttl > 0 {
		upsert += " USING TTL ?"
		ttlArgs = []any{int(ttl / time.Second)}
	}
	merged := g.compacted
	for start := 0; start < len(g.keys); start += compactChunk {
		end := min(start+compactChunk, len(g.keys))
		next, err := mergePartials(merged, g.facts[start:end])
		if err != nil {
			return err
		}
		batch := s.session.Batch(cassandra.LoggedBatch).WithContext(ctx)
		batch.Query(upsert, append([]any{org, cube, bucket, g.ts, dimsKey, g.dims, next}, ttlArgs...)...)
		for _, key := range g.keys[start:end] {
			batch.Query("DELETE FROM "+table+
				" WHERE org_id = ? AND cube = ? AND bucket = ? AND ts = ? AND dims_key = ? AND idempotency_key = ?",
				org, cube, bucket, g.ts, dimsKey, key)
		}
		if err := s.session.ExecuteBatch(batch); err != nil {
			return err
		}
		merged = next
	}
	return nil
}

// mergePartials merges base and every row's partials, measure by measure.
func mergePartials(base map[string][]byte, rows []map[string][]byte) (map[string][]byte, error) {
	acc := map[string]vizql.Partial{}
	add := func(partials map[string][]byte) error {
		for name, blob := range partials {
			p, err := vizql.DecodePartial(blob)
			if err != nil {
				return err
			}
			cur := acc[name]
			cur.Merge(p)
			acc[name] = cur
		}
		return nil
	}
	if err := add(base); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if err := add(row); err != nil {
			return nil, err
		}
	}
	out := make(map[string][]byte, len(acc))
	for name, p := range acc {
		out[name] = vizql.EncodePartial(p)
	}
	return out, nil
}
