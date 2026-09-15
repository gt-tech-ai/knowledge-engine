// Package storagekey is the single source of truth for the S3 object-key layout of an uploaded
// document. Both the API service (at upload time) and the document-events worker (when materializing
// connector-synced documents) must produce byte-identical keys for the same document, because the
// reclassify KB re-stamp, the download presign, and the S3 delete all address the object by this key —
// a divergence silently breaks those operations for every affected document.
//
// The two producers live in different apps and app→app imports are forbidden, so the format lives here
// in the foundation tier where both may depend on it.
package storagekey

import (
	"strings"

	"github.com/google/uuid"
)

// Build returns the per-org S3 object key for a document: "{orgExternalID}/{workspaceID}/{documentID}/{filename}".
// The org-prefixed layout lets each tenant's Bedrock Knowledge Base scan only its own "{org}/" slice
// Callers pass the filename verbatim (it is the display title, not sanitized here).
func Build(
	orgExternalID string,
	workspaceID, documentID uuid.UUID,
	filename string,
) string {
	return orgExternalID + "/" + workspaceID.String() + "/" + documentID.String() + "/" + filename
}

// OrgFrom returns the org (leading) segment of an org-prefixed storage key. The chunk-child key builder
// uses it to inherit the parent's org: the parent is org-prefixed, so its first segment is
// the org and the child lands under the same "{org}/" slice the per-org KB scans.
func OrgFrom(key string) string {
	head, _, _ := strings.Cut(key, "/")
	return head
}
