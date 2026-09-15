// Package services is the config surface for the domain (services) layer
// the document business limits — upload size ceiling and
// bulk-count cap — that were scattered consts. Defaults equal today's consts, so
// adoption is behavior-preserving. Pure data; the provider feeds these into the
// service + workflow (the all-or-nothing authority) and the plumbed UploadParams.domainRules
// path. (Filename length and the format allow-list remain fixed domain invariants —
// domain.MaxFilenameBytes and extensionFormats — not yet tunable by config.)
package services

import apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"

// DocumentConfig tunes the document domain-service business limits.
type DocumentConfig struct {
	// MaxUploadBytes is the single-document upload size ceiling (was 5 GiB).
	MaxUploadBytes int64 `mapstructure:"max_upload_bytes"`

	// MaxBulkCount is the maximum files in one bulk-upload request (was 100).
	MaxBulkCount int `mapstructure:"max_bulk_count"`
}

// Config tunes the domain (services) layer.
type Config struct {
	// Document holds the document-service business limits.
	Document DocumentConfig `mapstructure:"document"`
}

// DefaultConfig returns defaults identical to today's domain consts
// (MaxDocumentSizeBytes 5 GiB, MaxUploadBatchSize 100), so adoption is
// behavior-preserving.
func DefaultConfig() Config {
	return Config{
		Document: DocumentConfig{
			MaxUploadBytes: 5 << 30,
			MaxBulkCount:   100,
		},
	}
}

// Validate rejects non-positive document limits.
func (c Config) Validate() error {
	if c.Document.MaxUploadBytes < 1 {
		return apperr.InvalidInput("services.document.max_upload_bytes must be >= 1")
	}
	if c.Document.MaxBulkCount < 1 {
		return apperr.InvalidInput("services.document.max_bulk_count must be >= 1")
	}
	return nil
}
