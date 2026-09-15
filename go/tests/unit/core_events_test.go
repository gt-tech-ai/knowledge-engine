// Package core_test verifies the pkg/go/core/events package: domain event
// construction, JSON marshal/unmarshal round-trips, ParseEvent dispatch, and
// error handling for unknown or malformed event bodies. No network I/O occurs.
package unit_test

import (
	"encoding/json"
	"testing"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine/go/core/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDocumentUploadedEventMarshal tests that DocumentUploadedEvent serialises
// all fields to JSON with the correct key names.
//
// Why this test is important:
//   - Consumer services read these exact JSON keys from SQS messages; a renamed
//     field causes silent zero-value deserialization and dropped events
//
// What it tests:
//   - JSON output contains event_type="document.uploaded", document_id, org_id
func TestDocumentUploadedEventMarshal(t *testing.T) {
	t.Parallel()

	event := &events.DocumentUploadedEvent{
		EventMetadata: events.EventMetadata{
			EventID:    "evt-123",
			EventType:  events.EventDocumentUploaded,
			OccurredAt: time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC),
			OrgID:      "org-1",
			UserID:     "user-1",
		},
		DocumentID:  "doc-456",
		WorkspaceID: "ws-789",
		FileName:    "report.pdf",
		ContentType: "application/pdf",
		SizeBytes:   1024000,
		StorageKey:  "org-1/ws-789/doc-456/report.pdf",
	}

	data, err := json.Marshal(event)
	require.NoError(t, err, "event should marshal without error")

	var parsed map[string]any
	require.NoError(
		t,
		json.Unmarshal(data, &parsed),
		"marshaled event should unmarshal to map",
	)

	assert.Equal(t, "document.uploaded", parsed["event_type"])
	assert.Equal(t, "doc-456", parsed["document_id"])
	assert.Equal(t, "org-1", parsed["org_id"])
}

// TestDocumentUploadedEventUnmarshal tests that a JSON payload produced by the
// ingestion service decodes back into a DocumentUploadedEvent with all fields
// populated.
//
// Why this test is important:
//   - Mismatched JSON field names produce silent zero values that corrupt the
//     document pipeline without raising errors
//
// What it tests:
//   - EventID, DocumentID, and SizeBytes are correctly decoded from a JSON string
func TestDocumentUploadedEventUnmarshal(t *testing.T) {
	t.Parallel()

	data := `{
		"event_id": "evt-123",
		"event_type": "document.uploaded",
		"occurred_at": "2026-04-20T12:00:00Z",
		"org_id": "org-1",
		"user_id": "user-1",
		"document_id": "doc-456",
		"workspace_id": "ws-789",
		"file_name": "report.pdf",
		"content_type": "application/pdf",
		"size_bytes": 1024000,
		"storage_key": "org-1/ws-789/doc-456/report.pdf"
	}`

	var event events.DocumentUploadedEvent
	require.NoError(
		t,
		json.Unmarshal([]byte(data), &event),
		"event should unmarshal without error",
	)

	assert.Equal(t, "evt-123", event.EventID)
	assert.Equal(t, "doc-456", event.DocumentID)
	assert.Equal(t, int64(1024000), event.SizeBytes)
}

// TestParseEventDispatch tests that ParseEvent returns the correct concrete
// event type for each registered event_type string.
//
// Why this test is important:
//   - Ingestion and notification workers route events based on the returned
//     type; a wrong dispatch silently drops or misroutes every event
//
// What it tests:
//   - ParseEvent returns the matching concrete type for document.uploaded,
//     document.status_changed, document.deleted, team.created, and connector.sync_completed
func TestParseEventDispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      string
		eventType events.EventType
	}{
		{
			name:      "document.uploaded",
			data:      `{"event_id":"e1","event_type":"document.uploaded","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1","document_id":"d1","workspace_id":"w1","file_name":"test.pdf","content_type":"application/pdf","size_bytes":100,"storage_key":"k1"}`,
			eventType: events.EventDocumentUploaded,
		},
		{
			name:      "document.status_changed",
			data:      `{"event_id":"e2","event_type":"document.status_changed","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1","document_id":"d1","workspace_id":"w1","old_status":"pending","new_status":"processing"}`,
			eventType: events.EventDocumentStatusChanged,
		},
		{
			name:      "document.deleted",
			data:      `{"event_id":"e3","event_type":"document.deleted","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1","document_id":"d1","workspace_id":"w1"}`,
			eventType: events.EventDocumentDeleted,
		},
		{
			name:      "team.created",
			data:      `{"event_id":"e4","event_type":"team.created","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1","team_id":"t1","team_name":"Engineering"}`,
			eventType: events.EventTeamCreated,
		},
		{
			name:      "connector.sync_completed",
			data:      `{"event_id":"e5","event_type":"connector.sync_completed","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1","connector_id":"c1","workspace_id":"w1","documents_new":5,"documents_updated":2,"documents_deleted":1,"errors":0}`,
			eventType: events.EventConnectorSyncCompleted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event, err := events.ParseEvent([]byte(tt.data))
			require.NoError(t, err, "ParseEvent should succeed for %s", tt.name)
			assert.Equal(t, tt.eventType, event.Type())
		})
	}
}

// TestParseEventDocumentDeletedCarriesFacetFields tests that a document.deleted payload
// projects its title + classification onto the concrete event.
//
// Why this test is important:
//   - The value-suggestion facet's refcount DEC recovers the deleted value +
//     its classification straight from the event, so the delete never needs a second read
//     of the soft-deleted row; if these fields were dropped in ParseEvent the DEC would
//     target the wrong (empty) facet key and leak a phantom value.
//
// What it tests:
//   - ParseEvent of a document.deleted payload returns a DocumentDeletedEvent whose Title
//     and Classification match the wire fields.
func TestParseEventDocumentDeletedCarriesFacetFields(t *testing.T) {
	t.Parallel()

	data := `{"event_id":"e3","event_type":"document.deleted","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1","document_id":"d1","workspace_id":"w1","title":"Q3 Roadmap","classification":"restricted"}`

	event, err := events.ParseEvent([]byte(data))
	require.NoError(t, err)
	deleted, ok := event.(*events.DocumentDeletedEvent)
	require.True(t, ok, "expected *DocumentDeletedEvent, got %T", event)
	assert.Equal(t, "Q3 Roadmap", deleted.Title)
	assert.Equal(t, "restricted", deleted.Classification)
	assert.Equal(t, "w1", deleted.WorkspaceID)
	assert.Equal(t, "o1", deleted.OrgID)
}

// TestParseEventDocumentReclassifiedCarriesNewValues tests that a document.reclassified event
// parses into a *DocumentReclassifiedEvent carrying the NEW classification/trust + the storage key.
//
// Why this test is important:
//   - This event is the cross-service wire contract the Python ingestion consumer parses to re-stamp
//
// the Bedrock KB sidecar; the storage_key locates the sidecar and classification is
//
//	the value retrieval's clearance filter reads, so a parse gap silently drops the security re-sync.
//
// What it tests:
//   - ParseEvent dispatches event_type "document.reclassified" to *DocumentReclassifiedEvent with the
//     document_id, workspace_id, storage_key, classification, and trust_level projected from the envelope.
func TestParseEventDocumentReclassifiedCarriesNewValues(t *testing.T) {
	t.Parallel()

	data := `{"event_id":"e4","event_type":"document.reclassified","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1","document_id":"d1","workspace_id":"w1","storage_key":"o1/w1/d1/file.pdf","classification":"restricted","trust_level":"high"}`

	event, err := events.ParseEvent([]byte(data))
	require.NoError(t, err)
	reclassified, ok := event.(*events.DocumentReclassifiedEvent)
	require.True(t, ok, "expected *DocumentReclassifiedEvent, got %T", event)
	assert.Equal(t, "d1", reclassified.DocumentID)
	assert.Equal(t, "w1", reclassified.WorkspaceID)
	assert.Equal(t, "o1/w1/d1/file.pdf", reclassified.StorageKey)
	assert.Equal(t, "restricted", reclassified.Classification)
	assert.Equal(t, "high", reclassified.TrustLevel)
	assert.Equal(t, events.EventDocumentReclassified, reclassified.Type())
}

// TestParseEventUnknownType tests that ParseEvent returns an InvalidInput error
// for unrecognised event_type values.
//
// Why this test is important:
//   - Workers must reject unknown events explicitly; silent discard or panic
//     makes schema evolution invisible and allows corrupt messages to linger
//
// What it tests:
//   - ParseEvent with event_type="unknown.event" returns a CodeInvalidInput error
func TestParseEventUnknownType(t *testing.T) {
	t.Parallel()

	data := `{"event_id":"e1","event_type":"unknown.event","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1"}`

	_, err := events.ParseEvent([]byte(data))
	require.Error(t, err, "ParseEvent should return error for unknown event type")
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput),
		"expected CodeInvalidInput, got %v", apperr.Code(err))
}

// TestEventRoundTrip tests that a ConnectorSyncCompletedEvent survives a
// marshal -> ParseEvent cycle with all fields intact.
//
// Why this test is important:
//   - Optional metadata like CorrelationID used for distributed tracing must
//     survive the round-trip; silent loss would break trace correlation in production
//
// What it tests:
//   - Marshaled event re-parses to *ConnectorSyncCompletedEvent
//   - ConnectorID, DocumentsNew, and CorrelationID match the original
func TestEventRoundTrip(t *testing.T) {
	t.Parallel()

	original := &events.ConnectorSyncCompletedEvent{
		EventMetadata: events.EventMetadata{
			EventID:       "evt-rt",
			EventType:     events.EventConnectorSyncCompleted,
			OccurredAt:    time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC),
			OrgID:         "org-1",
			CorrelationID: "corr-abc",
		},
		ConnectorID:  "conn-1",
		WorkspaceID:  "ws-1",
		DocumentsNew: 10,
		DocumentsUpd: 3,
		DocumentsDel: 1,
		Errors:       0,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal should succeed")

	parsed, err := events.ParseEvent(data)
	require.NoError(t, err, "ParseEvent should succeed on marshaled data")

	syncEvent, ok := parsed.(*events.ConnectorSyncCompletedEvent)
	require.True(
		t,
		ok,
		"parsed event should be *ConnectorSyncCompletedEvent, got %T",
		parsed,
	)

	assert.Equal(t, "conn-1", syncEvent.ConnectorID)
	assert.Equal(t, 10, syncEvent.DocumentsNew)
	assert.Equal(t, "corr-abc", syncEvent.Metadata().CorrelationID)
}

// TestParseEvent_InvalidEventBody tests that ParseEvent returns an error when
// the event_type is valid but the body fails to unmarshal into the target struct.
//
// Why this test is important:
//   - Silently processing a corrupt message (e.g. bad timestamp) would propagate
//     zero-value data through the pipeline and corrupt downstream state
//
// What it tests:
//   - ParseEvent with event_type set but occurred_at="bad" returns CodeInvalidInput
//     for all five registered event types
func TestParseEvent_InvalidEventBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{
			"document.uploaded",
			`{"event_type":"document.uploaded","occurred_at":"bad"}`,
		},
		{
			"document.status_changed",
			`{"event_type":"document.status_changed","occurred_at":"bad"}`,
		},
		{
			"document.deleted",
			`{"event_type":"document.deleted","occurred_at":"bad"}`,
		},
		{
			"team.created",
			`{"event_type":"team.created","occurred_at":"bad"}`,
		},
		{
			"connector.sync_completed",
			`{"event_type":"connector.sync_completed","occurred_at":"bad"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := events.ParseEvent([]byte(tt.data))
			require.Error(t, err, "expected error for invalid event body in %s", tt.name)
			assert.True(t, apperr.Is(err, apperr.CodeInvalidInput),
				"expected CodeInvalidInput, got %v", apperr.Code(err))
		})
	}
}

// TestParseEvent_DocumentUploadedFieldFidelity tests that ParseEvent projects
// every field of a document.uploaded payload into the concrete event.
//
// Why this test is important:
//   - #9 replaced the per-type json.Unmarshal with a single envelope decode that
//     hand-copies each field into the concrete struct; a copy-paste slip there
//     would silently drop or cross-wire a field (e.g. StorageKey ← FileName),
//     corrupting the ingestion sidecar that indexes on these exact values
//   - DocumentUploadedEvent has the most fields (9), so it is the highest-risk
//     projection to pin
//
// What it tests:
//   - A fully populated document.uploaded payload round-trips through ParseEvent
//     with every field (ids, names, trust/classification, size) intact
func TestParseEvent_DocumentUploadedFieldFidelity(t *testing.T) {
	t.Parallel()

	original := &events.DocumentUploadedEvent{
		EventMetadata: events.EventMetadata{
			EventID:       "evt-du",
			EventType:     events.EventDocumentUploaded,
			OccurredAt:    time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC),
			OrgID:         "org-1",
			CorrelationID: "corr-du",
			TraceParent:   "00-trace-span-01",
		},
		DocumentID:     "doc-1",
		WorkspaceID:    "ws-1",
		FileName:       "report.pdf",
		ContentType:    "application/pdf",
		StorageKey:     "orgs/org-1/report.pdf",
		TrustLevel:     "verified",
		Classification: "confidential",
		SizeBytes:      4096,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal should succeed")

	parsed, err := events.ParseEvent(data)
	require.NoError(t, err, "ParseEvent should succeed")

	got, ok := parsed.(*events.DocumentUploadedEvent)
	require.True(t, ok, "parsed event should be *DocumentUploadedEvent, got %T", parsed)
	assert.Equal(t, original, got, "every field must survive the envelope projection")
}

// BenchmarkParseEvent measures ParseEvent on a document.uploaded payload so the
// #9 single-decode envelope can be compared against the previous two-parse
// implementation (Done clause: one decode, no small-input regression).
func BenchmarkParseEvent(b *testing.B) {
	data := []byte(
		`{"event_id":"e1","event_type":"document.uploaded","occurred_at":"2026-04-20T12:00:00Z","org_id":"o1","document_id":"d1","workspace_id":"w1","file_name":"test.pdf","content_type":"application/pdf","size_bytes":100,"storage_key":"k1","trust_level":"verified","classification":"public"}`,
	)
	b.ReportAllocs()
	for range b.N {
		if _, err := events.ParseEvent(data); err != nil {
			b.Fatal(err)
		}
	}
}
