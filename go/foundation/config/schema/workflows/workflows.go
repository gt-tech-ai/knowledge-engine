// Package workflows is the config surface for the workflow (orchestration) layer:
// the per-workflow operation timeout applied through workflow.Builder.WithTimeout.
// Default is no timeout. Pure data.
package workflows

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// Config tunes the workflow layer.
type Config struct {
	// Timeout is the per-workflow operation timeout (0 = no timeout, the default).
	Timeout time.Duration `mapstructure:"timeout"`
}

// DefaultConfig returns the workflow default: no per-workflow timeout.
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
