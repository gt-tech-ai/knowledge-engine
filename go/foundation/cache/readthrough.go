package cache

import (
	"context"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// ReadThrough is the shared read-through (cache-aside) primitive for a value of type T, folding the
// versioned envelope (Encode/Decode) and single-flight (a stampede of concurrent misses on one key
// collapses to ONE load) into one reuse point for every SCOPED cache decorator (suggest, count,
// …). Its shape mirrors the repos-tier caching decorator, which predates this primitive and
// still holds its own copy of the envelope + stampede logic; consolidating that decorator onto
// ReadThrough is a future DRY cleanup.
//
// Flow: a version-matching hit returns the decoded value WITHOUT calling load; otherwise a single
// load (per key, across concurrent callers) runs, its result is encoded and stored with ttl, and
// returned. A load error propagates and nothing is cached (a cache Get/Set failure degrades to the
// live load, never an error).
//
// T is stored and returned BY VALUE (the envelope round-trips a value), so concurrent callers that
// share the single-flight result never alias a mutable pointer — pass a value type (e.g.
// types.Page[...] or a small count struct), not a pointer.
func ReadThrough[T any](
	ctx context.Context,
	c interfaces.ByteCache,
	sf *singleflight.Group,
	key string,
	ttl time.Duration,
	version int,
	load func() (T, error),
) (T, error) {
	if data, hit := c.Get(ctx, key); hit {
		if val, ok, decErr := Decode[T](data, version); decErr == nil && ok {
			return val, nil
		}
	}
	v, err, _ := sf.Do(key, func() (any, error) {
		result, loadErr := load()
		if loadErr != nil {
			return nil, loadErr
		}
		if encoded, encErr := Encode(result, version); encErr == nil {
			c.Set(ctx, key, encoded, ttl)
		}
		return result, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	// sf.Do returns the exact value our load closure produced (a T), so the assertion is total.
	if result, ok := v.(T); ok {
		return result, nil
	}
	var zero T
	return zero, nil
}
