// Package events provides domain event types and parsing.
package events

import (
	"encoding/json"
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// EventType represents the type of a domain event.
type EventType string

// Event types.
const (
	// EventDocumentUploaded is emitted when a document is uploaded.
	EventDocumentUploaded EventType = "document.uploaded"
	// EventDocumentStatusChanged is emitted when a document's status transitions.
	EventDocumentStatusChanged EventType = "document.status_changed"
	// EventDocumentDeleted is emitted when a document is deleted.
	EventDocumentDeleted EventType = "document.deleted"
	// EventDocumentReclassified is emitted when an admin changes a document's
	// classification and/or trust level. The ingestion worker consumes it
	// to re-stamp the Bedrock KB metadata sidecar(s) and re-sync the document so
	// retrieval's clearance filter sees the new classification.
	EventDocumentReclassified EventType = "document.reclassified"
	// EventNotificationRequested is emitted by a producer to REQUEST a notification (the single,
	// reusable notification-emission seam). Its outbox payload is a notification event carrying the
	// recipient's user_id (NOT their email — the notification worker resolves the address from identity,
	// so no producer needs users-table access; service-to-table isolation). The document-events relay
	// routes it verbatim to the notification queue for the notification worker to dispatch.
	EventNotificationRequested EventType = "notification.requested"
	// EventTeamCreated is emitted when a team is created.
	EventTeamCreated EventType = "team.created"
	// EventConnectorSyncCompleted is emitted when a connector sync finishes.
	EventConnectorSyncCompleted EventType = "connector.sync_completed"
)

// Event is the interface all events implement.
type Event interface {
	// Type returns the event's type discriminator.
	Type() EventType

	// Metadata returns the common metadata shared by all events.
	Metadata() EventMetadata
}

// EventMetadata holds common fields for all events.
type EventMetadata struct {
	// EventID is the unique identifier of the event.
	EventID string `json:"event_id"`

	// EventType is the type discriminator for the event.
	EventType EventType `json:"event_type"`

	// OccurredAt is the timestamp when the event occurred.
	OccurredAt time.Time `json:"occurred_at"`

	// OrgID is the organization that the event belongs to.
	OrgID string `json:"org_id"`

	// UserID is the user who triggered the event, if applicable.
	UserID string `json:"user_id,omitempty"`

	// CorrelationID links related events across services.
	CorrelationID string `json:"correlation_id,omitempty"`

	// TraceParent carries the W3C trace context (traceparent header) captured when
	// the event was produced, so a consumer can continue the originating trace —
	// e.g. HTTP upload → outbox → relay publish → ingestion — as one distributed
	// trace. Empty when no active trace context was present.
	TraceParent string `json:"traceparent,omitempty"`
}

// DocumentUploadedEvent is emitted when a document is uploaded.
//
// Field order is chosen for struct alignment (all strings, then the int64 last); each field carries a
// doc comment per the documentation standard. The JSON tags are the wire contract the Python ingestion
// consumer (core/domain/events.py) parses — keep the two in sync.
type DocumentUploadedEvent struct {
	// EventMetadata contains the common event fields.
	EventMetadata

	// DocumentID is the identifier of the uploaded document.
	DocumentID string `json:"document_id"`

	// WorkspaceID is the workspace the document was uploaded to.
	WorkspaceID string `json:"workspace_id"`

	// FileName is the original name of the uploaded file.
	FileName string `json:"file_name"`

	// ContentType is the MIME type of the uploaded file.
	ContentType string `json:"content_type"`

	// StorageKey is the object storage key where the file is stored.
	StorageKey string `json:"storage_key"`

	// TrustLevel is the document's provenance trust; ingestion writes it into the
	// Bedrock KB sidecar for retrieval weighting.
	TrustLevel string `json:"trust_level"`

	// Classification is the document's sensitivity; ingestion writes it into the
	// Bedrock KB sidecar for clearance-based retrieval filtering.
	Classification string `json:"classification"`

	// SizeBytes is the size of the uploaded file in bytes.
	SizeBytes int64 `json:"size_bytes"`
}

// Type returns the document-uploaded event type.
func (e *DocumentUploadedEvent) Type() EventType { return EventDocumentUploaded }

// Metadata returns the embedded common event metadata.
func (e *DocumentUploadedEvent) Metadata() EventMetadata { return e.EventMetadata }

// DocumentStatusChangedEvent is emitted when a document's status changes.
type DocumentStatusChangedEvent struct {
	// EventMetadata contains the common event fields.
	EventMetadata

	// DocumentID is the identifier of the document whose status changed.
	DocumentID string `json:"document_id"`

	// WorkspaceID is the workspace the document belongs to.
	WorkspaceID string `json:"workspace_id"`

	// OldStatus is the previous status of the document.
	OldStatus string `json:"old_status"`

	// NewStatus is the new status of the document.
	NewStatus string `json:"new_status"`
}

// Type returns the document-status-changed event type.
func (e *DocumentStatusChangedEvent) Type() EventType { return EventDocumentStatusChanged }

// Metadata returns the embedded common event metadata.
func (e *DocumentStatusChangedEvent) Metadata() EventMetadata { return e.EventMetadata }

// DocumentDeletedEvent is emitted when a document is deleted.
type DocumentDeletedEvent struct {
	// EventMetadata contains the common event fields.
	EventMetadata

	// DocumentID is the identifier of the deleted document.
	DocumentID string `json:"document_id"`

	// WorkspaceID is the workspace the document belonged to.
	WorkspaceID string `json:"workspace_id"`

	// Title is the deleted document's name (its value in the "name" list-query field).
	// It is carried on the event so the value-suggestion facet's refcount DEC
	// recovers the value without re-reading the soft-deleted row.
	Title string `json:"title,omitempty"`

	// Classification is the deleted document's sensitivity — the facet partition key the
	// DEC decrements, carried alongside Title so the delete needs no second read.
	Classification string `json:"classification,omitempty"`
}

// Type returns the document-deleted event type.
func (e *DocumentDeletedEvent) Type() EventType { return EventDocumentDeleted }

// Metadata returns the embedded common event metadata.
func (e *DocumentDeletedEvent) Metadata() EventMetadata { return e.EventMetadata }

// DocumentReclassifiedEvent is emitted when an admin changes a document's classification
// and/or trust level. It is a re-sync TRIGGER: the ingestion worker re-stamps
// the Bedrock KB metadata sidecar(s) with the new Classification + TrustLevel and re-queues
// the document for indexing, so retrieval's clearance filter (which reads the KB metadata)
// stops seeing the stale classification. The from→to history is persisted in the
// document_reclassifications audit table, not carried here — the event needs only the new
// values plus the ids/key to locate and re-stamp the sidecar. The JSON tags are a subset of
// DocumentUploadedEvent's, so the Python ingestion consumer reuses the same sidecar path;
// keep the two in sync.
type DocumentReclassifiedEvent struct {
	// EventMetadata contains the common event fields.
	EventMetadata

	// DocumentID is the identifier of the reclassified document.
	DocumentID string `json:"document_id"`

	// WorkspaceID is the workspace the document belongs to (the KB tenant-isolation filter).
	WorkspaceID string `json:"workspace_id"`

	// StorageKey is the document's object-store key; the sidecar lives at
	// <storage_key>.metadata.json, so the consumer needs it to re-stamp the metadata.
	StorageKey string `json:"storage_key"`

	// TrustLevel is the document's NEW provenance trust to write into the sidecar.
	TrustLevel string `json:"trust_level"`

	// Classification is the document's NEW sensitivity to write into the sidecar (the
	// value retrieval's clearance filter compares against).
	Classification string `json:"classification"`
}

// Type returns the document-reclassified event type.
func (e *DocumentReclassifiedEvent) Type() EventType { return EventDocumentReclassified }

// Metadata returns the embedded common event metadata.
func (e *DocumentReclassifiedEvent) Metadata() EventMetadata { return e.EventMetadata }

// TeamCreatedEvent is emitted when a team is created.
type TeamCreatedEvent struct {
	// EventMetadata contains the common event fields.
	EventMetadata

	// TeamID is the identifier of the newly created team.
	TeamID string `json:"team_id"`

	// TeamName is the display name of the newly created team.
	TeamName string `json:"team_name"`
}

// Type returns the team-created event type.
func (e *TeamCreatedEvent) Type() EventType { return EventTeamCreated }

// Metadata returns the embedded common event metadata.
func (e *TeamCreatedEvent) Metadata() EventMetadata { return e.EventMetadata }

// ConnectorSyncCompletedEvent is emitted when a connector sync finishes.
type ConnectorSyncCompletedEvent struct {
	// EventMetadata contains the common event fields.
	EventMetadata

	// ConnectorID is the identifier of the connector that completed syncing.
	ConnectorID string `json:"connector_id"`

	// WorkspaceID is the workspace the connector synced into.
	WorkspaceID string `json:"workspace_id"`

	// DocumentsNew is the count of newly created documents.
	DocumentsNew int `json:"documents_new"`

	// DocumentsUpd is the count of updated documents.
	DocumentsUpd int `json:"documents_updated"`

	// DocumentsDel is the count of deleted documents.
	DocumentsDel int `json:"documents_deleted"`

	// Errors is the count of errors encountered during the sync.
	Errors int `json:"errors"`
}

// Type returns the connector-sync-completed event type.
func (e *ConnectorSyncCompletedEvent) Type() EventType { return EventConnectorSyncCompleted }

// Metadata returns the embedded common event metadata.
func (e *ConnectorSyncCompletedEvent) Metadata() EventMetadata { return e.EventMetadata }

// eventEnvelope decodes any event payload in a single pass (#9). The embedded
// EventMetadata carries the discriminant (event_type) and the fields common to
// every event; the remaining fields are the union of all concrete event bodies.
// The wire format is flat (each field promoted to the top-level JSON object — the
// cross-language contract the Python consumers parse), so there is no separable
// body to capture as a json.RawMessage; the union lets ParseEvent unmarshal the
// whole payload ONCE and then project it into the matching concrete Event, instead
// of the previous two full reflection parses (a type sniff, then the concrete
// struct) — 2N→N parses on the connector-sync / SQS consume path. A well-formed
// payload only carries its own type's keys, so the unused fields stay zero-valued.
// No key appears across two event types with differing Go types, so no field
// collides. Adding a new event type means adding its fields here and a case below.
type eventEnvelope struct {
	// EventMetadata carries event_type (the discriminant) plus the fields common
	// to every event.
	EventMetadata

	// NewStatus is the new document status (document.status_changed).
	NewStatus string `json:"new_status,omitempty"`
	// TeamID is the created team's id (team.created).
	TeamID string `json:"team_id,omitempty"`
	// FileName is the uploaded file's original name (document.uploaded).
	FileName string `json:"file_name,omitempty"`
	// Title is the deleted document's name (document.deleted) — the facet DEC value.
	Title string `json:"title,omitempty"`
	// DocumentID identifies the document (document.uploaded/status_changed/deleted).
	DocumentID string `json:"document_id,omitempty"`
	// StorageKey is the uploaded file's object-store key (document.uploaded).
	StorageKey string `json:"storage_key,omitempty"`
	// TrustLevel is the document's provenance trust (document.uploaded).
	TrustLevel string `json:"trust_level,omitempty"`
	// Classification is the document's sensitivity (document.uploaded).
	Classification string `json:"classification,omitempty"`
	// WorkspaceID is the owning workspace (document and connector events).
	WorkspaceID string `json:"workspace_id,omitempty"`
	// OldStatus is the previous document status (document.status_changed).
	OldStatus string `json:"old_status,omitempty"`
	// ContentType is the uploaded file's MIME type (document.uploaded).
	ContentType string `json:"content_type,omitempty"`
	// TeamName is the created team's display name (team.created).
	TeamName string `json:"team_name,omitempty"`
	// ConnectorID identifies the synced connector (connector.sync_completed).
	ConnectorID string `json:"connector_id,omitempty"`
	// DocumentsUpd is the count of updated documents (connector.sync_completed).
	DocumentsUpd int `json:"documents_updated,omitempty"`
	// DocumentsDel is the count of deleted documents (connector.sync_completed).
	DocumentsDel int `json:"documents_deleted,omitempty"`
	// Errors is the count of errors during the sync (connector.sync_completed).
	Errors int `json:"errors,omitempty"`
	// DocumentsNew is the count of newly created documents (connector.sync_completed).
	DocumentsNew int `json:"documents_new,omitempty"`
	// SizeBytes is the uploaded file size in bytes (document.uploaded).
	SizeBytes int64 `json:"size_bytes,omitempty"`
}

// ParseEvent parses a JSON event payload and returns the appropriate Event type.
// It unmarshals the payload exactly once (into eventEnvelope) and dispatches on
// event_type, rather than parsing the same bytes twice (#9).
func ParseEvent(data []byte) (Event, error) {
	var env eventEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse event")
	}

	switch env.EventType {
	case EventDocumentUploaded:
		return &DocumentUploadedEvent{
			EventMetadata:  env.EventMetadata,
			DocumentID:     env.DocumentID,
			WorkspaceID:    env.WorkspaceID,
			FileName:       env.FileName,
			ContentType:    env.ContentType,
			StorageKey:     env.StorageKey,
			TrustLevel:     env.TrustLevel,
			Classification: env.Classification,
			SizeBytes:      env.SizeBytes,
		}, nil
	case EventDocumentStatusChanged:
		return &DocumentStatusChangedEvent{
			EventMetadata: env.EventMetadata,
			DocumentID:    env.DocumentID,
			WorkspaceID:   env.WorkspaceID,
			OldStatus:     env.OldStatus,
			NewStatus:     env.NewStatus,
		}, nil
	case EventDocumentDeleted:
		return &DocumentDeletedEvent{
			EventMetadata:  env.EventMetadata,
			DocumentID:     env.DocumentID,
			WorkspaceID:    env.WorkspaceID,
			Title:          env.Title,
			Classification: env.Classification,
		}, nil
	case EventDocumentReclassified:
		return &DocumentReclassifiedEvent{
			EventMetadata:  env.EventMetadata,
			DocumentID:     env.DocumentID,
			WorkspaceID:    env.WorkspaceID,
			StorageKey:     env.StorageKey,
			TrustLevel:     env.TrustLevel,
			Classification: env.Classification,
		}, nil
	case EventTeamCreated:
		return &TeamCreatedEvent{
			EventMetadata: env.EventMetadata,
			TeamID:        env.TeamID,
			TeamName:      env.TeamName,
		}, nil
	case EventConnectorSyncCompleted:
		return &ConnectorSyncCompletedEvent{
			EventMetadata: env.EventMetadata,
			ConnectorID:   env.ConnectorID,
			WorkspaceID:   env.WorkspaceID,
			DocumentsNew:  env.DocumentsNew,
			DocumentsUpd:  env.DocumentsUpd,
			DocumentsDel:  env.DocumentsDel,
			Errors:        env.Errors,
		}, nil
	default:
		return nil, apperr.InvalidInput(
			fmt.Sprintf("unknown event type: %s", env.EventType),
		)
	}
}
