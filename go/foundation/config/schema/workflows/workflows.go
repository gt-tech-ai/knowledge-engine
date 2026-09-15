// Package workflows is the config surface for the workflow (orchestration) layer
// the per-workflow operation timeout that the
// workflow.Builder.WithTimeout support exposes but no app sets today. Default is
// no timeout (behavior-preserving). Pure data.
package workflows

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// Config tunes the workflow layer.
type Config struct {
	// Timeout is the per-workflow operation timeout (0 = no timeout, today's
	// behavior).
	Timeout time.Duration `mapstructure:"timeout"`
}

// DefaultConfig returns the behavior-preserving default (no per-workflow timeout).
func DefaultConfig() Config {
	return Config{}
}

// Validate rejects a negative timeout.
func (c Config) Validate() error {
	if c.Timeout < 0 {
		return apperr.InvalidInput("workflows.timeout must be >= 0")
	}
	return nil
}
