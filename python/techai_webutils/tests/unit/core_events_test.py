"""Unit tests for Python core domain events.

This file tests event serialization (Pydantic model_dump/model_validate), JSON
round-trips, parse_event dispatch routing, and metadata extraction. Events must
serialize identically to Go for cross-service compatibility over SQS/Kafka.

# Test Coverage

The tests cover:
  - Serialization: JSON serialization and deserialization for each event type
  - Round-trip fidelity: serialize then deserialize without data loss
  - Event dispatch: parse_event routing by event_type string
  - Input formats: dict, JSON string, and bytes inputs
  - Error handling: unknown type and missing type field
  - Metadata: extraction of envelope fields from event instances

# Test Structure

Tests use pytest class-based organization grouped by concern (serialization,
dispatch, metadata). No mocking is needed since these are pure Pydantic models.

# Running Tests

Run with: pytest tests/python/core/test_events.py
"""

from datetime import UTC, datetime
import json

from techai_webutils.core.events.events import (
    ConnectorSyncCompletedEvent,
    DocumentDeletedEvent,
    DocumentStatusChangedEvent,
    DocumentUploadedEvent,
    EventType,
    TeamCreatedEvent,
    TeamMemberAddedEvent,
    WorkspaceTeamGrantedEvent,
    parse_event,
)
import pytest


class TestEventSerialization:
    """Test suite for event model serialization and deserialization."""

    def test_document_uploaded_to_json(self):
        """Test that DocumentUploadedEvent serializes all fields via model_dump.

        **Why this test is important:**
          - Events are published to SQS/Kafka as JSON; missing fields cause consumer failures
          - The Go consumer expects specific field names and types in the payload
          - Document processing depends on all metadata being present in the event

        **What it tests:**
          - event_type field serializes to "document.uploaded"
          - document_id field is present in serialized output
          - org_id field is present in serialized output
          - size_bytes field preserves integer value
        """
        event = DocumentUploadedEvent(
            event_id="evt-123",
            event_type=EventType.DOCUMENT_UPLOADED,
            occurred_at=datetime(2026, 4, 20, 12, 0, 0, tzinfo=UTC),
            org_id="org-1",
            user_id="user-1",
            document_id="doc-456",
            workspace_id="ws-789",
            file_name="report.pdf",
            content_type="application/pdf",
            size_bytes=1024000,
            storage_key="org-1/ws-789/doc-456/report.pdf",
        )

        data = event.model_dump()
        assert data["event_type"] == "document.uploaded"
        assert data["document_id"] == "doc-456"
        assert data["org_id"] == "org-1"
        assert data["size_bytes"] == 1024000

    def test_document_uploaded_from_json(self):
        """Test that DocumentUploadedEvent deserializes from a raw dict.

        **Why this test is important:**
          - SQS message bodies arrive as dicts after JSON parsing
          - Pydantic validation ensures malformed messages are rejected early
          - Field type coercion (e.g., datetime strings) must work correctly

        **What it tests:**
          - event.document_id matches the input dict value
          - event.size_bytes matches the input dict integer value
        """
        raw = {
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
            "storage_key": "org-1/ws-789/doc-456/report.pdf",
        }

        event = DocumentUploadedEvent.model_validate(raw)
        assert event.document_id == "doc-456"
        assert event.size_bytes == 1024000

    def test_connector_sync_round_trip(self):
        """Test that events survive a full JSON round-trip without data loss.

        **Why this test is important:**
          - Events pass through multiple serialization boundaries (publisher, queue, consumer)
          - Silent field dropping would cause downstream processing errors
          - Optional fields like correlation_id must survive the round-trip

        **What it tests:**
          - connector_id is preserved after serialize/deserialize
          - documents_new integer is preserved after serialize/deserialize
          - correlation_id optional field is preserved after serialize/deserialize
        """
        original = ConnectorSyncCompletedEvent(
            event_id="evt-rt",
            event_type=EventType.CONNECTOR_SYNC_COMPLETED,
            occurred_at=datetime(2026, 4, 20, 12, 0, 0, tzinfo=UTC),
            org_id="org-1",
            correlation_id="corr-abc",
            connector_id="conn-1",
            workspace_id="ws-1",
            documents_new=10,
            documents_updated=3,
            documents_deleted=1,
            errors=0,
        )

        # Serialize
        json_str = original.model_dump_json()

        # Deserialize
        parsed = ConnectorSyncCompletedEvent.model_validate_json(json_str)
        assert parsed.connector_id == "conn-1"
        assert parsed.documents_new == 10
        assert parsed.correlation_id == "corr-abc"


class TestParseEvent:
    """Test suite for parse_event dispatch routing."""

    def test_dispatch_document_uploaded(self):
        """Test that parse_event returns DocumentUploadedEvent for 'document.uploaded'.

        **Why this test is important:**
          - parse_event is the single entry point for all event deserialization in consumers
          - Incorrect routing would deliver events to wrong handlers causing data corruption
          - The event_type string must match the Go publisher's output exactly

        **What it tests:**
          - Returned object is an instance of DocumentUploadedEvent
          - document_id field is correctly populated from input
        """
        data = {
            "event_id": "e1",
            "event_type": "document.uploaded",
            "occurred_at": "2026-04-20T12:00:00Z",
            "org_id": "o1",
            "document_id": "d1",
            "workspace_id": "w1",
            "file_name": "test.pdf",
            "content_type": "application/pdf",
            "size_bytes": 100,
            "storage_key": "k1",
        }
        event = parse_event(data)
        assert isinstance(event, DocumentUploadedEvent)
        assert event.document_id == "d1"

    def test_dispatch_document_status_changed(self):
        """Test that parse_event returns DocumentStatusChangedEvent for 'document.status_changed'.

        **Why this test is important:**
          - Status change events drive the document processing state machine
          - Incorrect dispatch would cause documents to get stuck in intermediate states
          - Both old_status and new_status must be correctly deserialized for transition validation

        **What it tests:**
          - Returned object is an instance of DocumentStatusChangedEvent
          - old_status field matches input value
          - new_status field matches input value
        """
        data = {
            "event_id": "e2",
            "event_type": "document.status_changed",
            "occurred_at": "2026-04-20T12:00:00Z",
            "org_id": "o1",
            "document_id": "d1",
            "workspace_id": "w1",
            "old_status": "pending",
            "new_status": "processing",
        }
        event = parse_event(data)
        assert isinstance(event, DocumentStatusChangedEvent)
        assert event.old_status == "pending"
        assert event.new_status == "processing"

    def test_dispatch_document_deleted(self):
        """Test that parse_event returns DocumentDeletedEvent for 'document.deleted'.

        **Why this test is important:**
          - Deletion events trigger cleanup in storage, vector DB, and search indexes
          - Missed deletion events leave orphaned data consuming resources
          - Must be dispatched correctly for GDPR/data retention compliance

        **What it tests:**
          - Returned object is an instance of DocumentDeletedEvent
        """
        data = {
            "event_id": "e3",
            "event_type": "document.deleted",
            "occurred_at": "2026-04-20T12:00:00Z",
            "org_id": "o1",
            "document_id": "d1",
            "workspace_id": "w1",
        }
        event = parse_event(data)
        assert isinstance(event, DocumentDeletedEvent)

    def test_dispatch_team_created(self):
        """Test that parse_event returns TeamCreatedEvent for 'team.created'.

        **Why this test is important:**
          - Team creation events bootstrap workspace access control
          - Authorization decisions depend on team membership propagation
          - team_name must be preserved for UI display and audit logging

        **What it tests:**
          - Returned object is an instance of TeamCreatedEvent
          - team_name field matches input value
        """
        data = {
            "event_id": "e4",
            "event_type": "team.created",
            "occurred_at": "2026-04-20T12:00:00Z",
            "org_id": "o1",
            "team_id": "t1",
            "team_name": "Engineering",
        }
        event = parse_event(data)
        assert isinstance(event, TeamCreatedEvent)
        assert event.team_name == "Engineering"

    def test_dispatch_connector_sync(self):
        """Test that parse_event returns ConnectorSyncCompletedEvent for 'connector.sync_completed'.

        **Why this test is important:**
          - Connector sync events trigger downstream document processing pipelines
          - Document counts drive progress reporting and billing calculations
          - Incorrect dispatch would leave synced documents unprocessed

        **What it tests:**
          - Returned object is an instance of ConnectorSyncCompletedEvent
          - documents_new field preserves the integer count
        """
        data = {
            "event_id": "e5",
            "event_type": "connector.sync_completed",
            "occurred_at": "2026-04-20T12:00:00Z",
            "org_id": "o1",
            "connector_id": "c1",
            "workspace_id": "w1",
            "documents_new": 5,
            "documents_updated": 2,
            "documents_deleted": 1,
            "errors": 0,
        }
        event = parse_event(data)
        assert isinstance(event, ConnectorSyncCompletedEvent)
        assert event.documents_new == 5

    def test_parse_from_json_string(self):
        """Test that parse_event accepts JSON string input.

        **Why this test is important:**
          - SQS and Kafka deliver message bodies as raw JSON strings
          - parse_event must handle string input without requiring callers to pre-parse
          - Avoids double-parsing bugs in consumer implementations

        **What it tests:**
          - Returned object is an instance of TeamMemberAddedEvent
          - role field is correctly deserialized from the JSON string
        """
        json_str = json.dumps(
            {
                "event_id": "e1",
                "event_type": "team.member_added",
                "occurred_at": "2026-04-20T12:00:00Z",
                "org_id": "o1",
                "team_id": "t1",
                "member_id": "m1",
                "role": "admin",
            }
        )
        event = parse_event(json_str)
        assert isinstance(event, TeamMemberAddedEvent)
        assert event.role == "admin"

    def test_parse_from_bytes(self):
        """Test that parse_event accepts bytes input.

        **Why this test is important:**
          - Raw message bodies from queues arrive as bytes before decoding
          - parse_event must handle bytes directly for zero-copy processing efficiency
          - Eliminates manual decode steps in consumer code

        **What it tests:**
          - Returned object is an instance of WorkspaceTeamGrantedEvent
          - permission field is correctly deserialized from bytes input
        """
        data = json.dumps(
            {
                "event_id": "e1",
                "event_type": "workspace.team_granted",
                "occurred_at": "2026-04-20T12:00:00Z",
                "org_id": "o1",
                "workspace_id": "w1",
                "team_id": "t1",
                "permission": "read",
            }
        ).encode()
        event = parse_event(data)
        assert isinstance(event, WorkspaceTeamGrantedEvent)
        assert event.permission == "read"

    def test_unknown_event_type_raises(self):
        """Test that parse_event raises ValueError for unrecognized event types.

        **Why this test is important:**
          - Fail-fast prevents silent message loss in consumers
          - Unknown event types indicate schema drift between services
          - Raising immediately surfaces integration issues during development

        **What it tests:**
          - ValueError is raised with "Unknown event type" in the message
        """
        data = {
            "event_id": "e1",
            "event_type": "unknown.event",
            "occurred_at": "2026-04-20T12:00:00Z",
            "org_id": "o1",
        }
        with pytest.raises(ValueError, match="Unknown event type"):
            parse_event(data)

    def test_missing_event_type_raises(self):
        """Test that parse_event raises ValueError when event_type field is missing.

        **Why this test is important:**
          - Messages without event_type cannot be routed and must be rejected
          - This guards against malformed messages from misconfigured publishers
          - Early detection prevents unprocessable messages from clogging the queue

        **What it tests:**
          - ValueError is raised with "Missing event_type" in the message
        """
        data = {"event_id": "e1", "org_id": "o1"}
        with pytest.raises(ValueError, match="Missing event_type"):
            parse_event(data)


class TestEventMetadata:
    """Test suite for event metadata extraction."""

    def test_metadata_extraction(self):
        """Test that metadata() returns envelope fields for logging and tracing.

        **Why this test is important:**
          - Observability depends on correlation_id propagation for distributed tracing
          - Log enrichment requires event_id and org_id for debugging production issues
          - Metadata must be extractable without accessing domain-specific event fields

        **What it tests:**
          - meta.event_id matches the event's event_id
          - meta.org_id matches the event's org_id
          - meta.user_id matches the event's user_id
          - meta.correlation_id matches the event's correlation_id
        """
        event = DocumentUploadedEvent(
            event_id="evt-123",
            event_type=EventType.DOCUMENT_UPLOADED,
            occurred_at=datetime(2026, 4, 20, 12, 0, 0, tzinfo=UTC),
            org_id="org-1",
            user_id="user-1",
            correlation_id="corr-xyz",
            document_id="doc-1",
            workspace_id="ws-1",
            file_name="test.pdf",
            content_type="application/pdf",
            size_bytes=100,
            storage_key="k1",
        )

        meta = event.metadata()
        assert meta.event_id == "evt-123"
        assert meta.org_id == "org-1"
        assert meta.user_id == "user-1"
        assert meta.correlation_id == "corr-xyz"
