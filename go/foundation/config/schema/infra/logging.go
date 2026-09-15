package infra

import (
	"fmt"
	"strings"

	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/foundation/logger"
)

// LoggingConfig holds logging configuration.
type LoggingConfig struct {
	// Kind selects the logger implementation ("zap" or "stdlib").
	Kind string `mapstructure:"kind"`

	// Level is the minimum log severity to emit (e.g. "debug", "info", "warn", "error").
	Level string `mapstructure:"level"`

	// Format controls the output encoding ("json" or "text").
	Format string `mapstructure:"format"`

	// RedactPII enables automatic redaction of personally identifiable information in log output.
	RedactPII bool `mapstructure:"redact_pii"`
}

// DefaultLoggingConfig returns a LoggingConfig with defaults.
func DefaultLoggingConfig() LoggingConfig {
	return LoggingConfig{
		Kind:      "zap",
		Level:     "info",
		Format:    "json",
		RedactPII: true,
	}
}

// Validate returns an error if the configuration is invalid.
func (c LoggingConfig) Validate() error {
	kind := strings.ToLower(c.Kind)
	if kind != "zap" && kind != "stdlib" {
		return coreerr.New(
			coreerr.CodeInvalidInput,
			fmt.Sprintf("unknown logger kind: %s", c.Kind),
		)
	}
	return nil
}

// GetKind converts the config kind string to a logger.Kind.
func (c *LoggingConfig) GetKind() (logger.Kind, error) {
	switch strings.ToLower(c.Kind) {
	case "zap":
		return logger.KindZap, nil
	case "stdlib":
		return logger.KindStdlib, nil
	default:
		return 0, coreerr.New(
			coreerr.CodeInvalidInput,
			fmt.Sprintf("unknown logger kind: %s", c.Kind),
		)
	}
}
