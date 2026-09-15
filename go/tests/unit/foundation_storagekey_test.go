package unit_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine/go/foundation/storagekey"
)

// TestStorageKey_BuildAndOrgFromRoundTrip tests that Build produces the per-org
// "{org}/{workspace}/{document}/{filename}" layout and OrgFrom recovers the leading org segment.
//
// Why this test is important:
//   - Build is the single source of truth both the API upload path and the connector-sync worker
//     depend on; if the two ever produced different keys for the same document, the reclassify KB
//     re-stamp, the download presign, and the S3 delete would all silently address the wrong object.
//     Pinning the exact layout here is what lets both callers safely delegate to one function.
//
// What it tests:
//   - Build renders the exact 4-segment org-prefixed key, and OrgFrom returns the org segment for a
//     key Build produced (round-trip), including when the filename itself contains slashes.
func TestStorageKey_BuildAndOrgFromRoundTrip(t *testing.T) {
	t.Parallel()

	ws := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	doc := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	key := storagekey.Build("acme", ws, doc, "report.pdf")
	require.Equal(t, "acme/"+ws.String()+"/"+doc.String()+"/report.pdf", key)
	require.Equal(t, "acme", storagekey.OrgFrom(key))

	// A filename with slashes must not shift the org segment (OrgFrom cuts on the FIRST separator).
	nested := storagekey.Build("acme", ws, doc, "a/b.txt")
	require.Equal(t, "acme", storagekey.OrgFrom(nested))
}
