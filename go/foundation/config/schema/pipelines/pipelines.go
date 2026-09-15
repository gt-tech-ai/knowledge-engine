// Package pipelines is the config surface for the pipeline (orchestration) layer
// the per-pipeline operation timeout and the document
// upload multipart sizing. maxMultipartParts (S3's 10 000-part protocol ceiling)
// is deliberately NOT a knob here — it is a protocol constant, guarded by a test.
// Defaults equal today's consts. Pure data.
package pipelines

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// UploadConfig tunes the multipart-upload sizing.
type UploadConfig struct {
	// MultipartThresholdBytes is the size at/above which an upload uses the
	// browser-direct multipart path (was 20 MiB).
	MultipartThresholdBytes int64 `mapstructure:"multipart_threshold_bytes"`

	// MultipartPartSizeBytes is the fixed size of every multipart part except the
	// last (was 16 MiB).
	MultipartPartSizeBytes int64 `mapstructure:"multipart_part_size_bytes"`
}

// Config tunes the pipeline layer.
type Config struct {
	// Upload tunes the multipart-upload sizing.
	Upload UploadConfig `mapstructure:"upload"`

	// Timeout is the per-pipeline operation timeout (0 = no timeout, today's
	// behavior — the pipeline builder's WithTimeout support that no app sets yet).
	Timeout time.Duration `mapstructure:"timeout"`
}

// DefaultConfig returns defaults identical to today's upload consts
// (MultipartThresholdBytes 20 MiB, MultipartPartSizeBytes 16 MiB) and no
// per-pipeline timeout, so adoption is behavior-preserving.
func DefaultConfig() Config {
	return Config{
		Upload: UploadConfig{
			MultipartThresholdBytes: 20 << 20,
			MultipartPartSizeBytes:  16 << 20,
		},
	}
}

// Validate rejects non-positive multipart sizes and a negative timeout.
func (c Config) Validate() error {
	if c.Upload.MultipartThresholdBytes < 1 {
		return apperr.InvalidInput(
			"pipelines.upload.multipart_threshold_bytes must be >= 1",
		)
	}
	if c.Upload.MultipartPartSizeBytes < 1 {
		return apperr.InvalidInput(
			"pipelines.upload.multipart_part_size_bytes must be >= 1",
		)
	}
	if c.Timeout < 0 {
		return apperr.InvalidInput("pipelines.timeout must be >= 0")
	}
	return nil
}
