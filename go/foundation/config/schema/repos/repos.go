// Package repos is the config surface for the repository (data-access) layer
// the cache TTL/version/key-prefix and the per-operation
// timeout that were const in the repo decorator wiring. The retry/breaker tuning
// the repo resilience stack uses is sourced from the schema/resilience surface
// not duplicated here. Defaults equal today's consts. Pure data.
package repos

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// CachingConfig tunes the repository caching decorator.
type CachingConfig struct {
	// KeyPrefix namespaces cache keys (empty = no prefix, today's behavior).
	KeyPrefix string `mapstructure:"key_prefix"`

	// TTL is how long a cached entity remains valid (was 5m).
	TTL time.Duration `mapstructure:"ttl"`

	// Version namespaces cache keys so a schema change invalidates them (was 1).
	Version int `mapstructure:"version"`
}

// CompilerConfig selects the query-compiler backend — the target query language the list stores
// compile a validated filter/sort into. "ent" compiles to Ent dialect/sql (Postgres);
// mongo / elastic / gorm are reserved for future backends. Which backend you get is a config change,
// not a code edit.
type CompilerConfig struct {
	// Kind selects the backend: "ent" (the only implemented target today).
	Kind string `mapstructure:"kind"`
}

// DefaultCompilerConfig returns the Ent/SQL backend, matching the base config.
func DefaultCompilerConfig() CompilerConfig {
	return CompilerConfig{Kind: "ent"}
}

// Validate rejects a kind that is not an implemented backend.
func (c CompilerConfig) Validate() error {
	if c.Kind != "ent" {
		return apperr.InvalidInput(`repos.compiler.kind must be "ent"`)
	}
	return nil
}

// Config tunes the repository decorator stack.
type Config struct {
	// Compiler selects the query-compiler backend the list stores use.
	Compiler CompilerConfig `mapstructure:"compiler"`

	// Caching tunes the repository cache decorator.
	Caching CachingConfig `mapstructure:"caching"`

	// Timeout bounds a single decorated repository operation (was 5s).
	Timeout time.Duration `mapstructure:"timeout"`
}

// DefaultConfig returns defaults identical to today's repo consts (cacheTTL 5m,
// cacheVersion 1, timeoutDuration 5s), so adoption is behavior-preserving.
func DefaultConfig() Config {
	return Config{
		Caching: CachingConfig{
			TTL:     5 * time.Minute,
			Version: 1,
		},
		Compiler: DefaultCompilerConfig(),
		Timeout:  5 * time.Second,
	}
}

// Validate rejects non-positive durations and a negative cache version.
func (c Config) Validate() error {
	if c.Caching.TTL < 0 {
		return apperr.InvalidInput("repos.caching.ttl must be >= 0")
	}
	if c.Caching.Version < 0 {
		return apperr.InvalidInput("repos.caching.version must be >= 0")
	}
	if c.Timeout < 0 {
		return apperr.InvalidInput("repos.timeout must be >= 0")
	}
	return c.Compiler.Validate()
}
