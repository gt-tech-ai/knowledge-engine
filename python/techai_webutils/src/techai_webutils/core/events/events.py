"""Domain event types matching the Go events package.

Both Go and Python services produce/consume the same JSON schema for events.
Events are published to SQS and consumed by various services.
"""

from datetime import UTC, datetime
from enum import StrEnum
import json
from typing import Annotated, Literal

from pydantic import BaseModel, Field


class EventType(StrEnum):
    """Event type identifiers matching Go's EventType constants."""

    DOCUMENT_UPLOADED = "document.uploaded"
    """Emitted when a document's bytes land in the object store, ready for ingestion."""
    DOCUMENT_STATUS_CHANGED = "document.status_changed"
    """Emitted when a document's ingestion/processing status transitions."""
    DOCUMENT_DELETED = "document.deleted"
    """Emitted when a document is removed from a workspace."""
    TEAM_CREATED = "team.created"
    """Emitted when a new team is created within an organization."""
    TEAM_MEMBER_ADDED = "team.member_added"
    """Emitted when a member is added to a team."""
    WORKSPACE_TEAM_GRANTED = "workspace.team_granted"
    """Emitted when a team is granted access to a workspace."""
    ORGANIZATION_MEMBER_ADDED = "organization.member_added"
    """Emitted when a member joins an organization."""
    CONNECTOR_SYNC_COMPLETED = "connector.sync_completed"
    """Emitted when a connector finishes a sync run."""


class EventMetadata(BaseModel):
    """Common metadata for all domain events."""

    event_id: str
    event_type: EventType
    occurred_at: datetime = Field(default_factory=lambda: datetime.now(UTC))
    org_id: str
    user_id: str = ""
    correlation_id: str = ""


class DomainEvent(BaseModel):
    """Base class for all domain events."""

    event_id: str
    event_type: EventType
    occurred_at: datetime = Field(default_factory=lambda: datetime.now(UTC))
    org_id: str
    user_id: str = ""
    correlation_id: str = ""

    def metadata(self) -> EventMetadata:
        """Extract event metadata."""
        return EventMetadata(
            event_id=self.event_id,
            event_type=self.event_type,
            occurred_at=self.occurred_at,
            org_id=self.org_id,
            user_id=self.user_id,
            correlation_id=self.correlation_id,
        )


# --- Document Events ---


class DocumentUploadedEvent(DomainEvent):
    """Published when a document is uploaded to a workspace."""

    event_type: Literal[EventType.DOCUMENT_UPLOADED] = EventType.DOCUMENT_UPLOADED  # type: ignore[assignment]
    """Discriminator fixing this envelope to the document.uploaded event type."""
    document_id: str
    """Identifier of the uploaded document."""
    workspace_id: str
    """Identifier of the workspace the document belongs to."""
    file_name: str
    """Original file name of the uploaded document."""
    content_type: str
    """MIME type of the uploaded file."""
    size_bytes: int
    """Size of the uploaded file in bytes."""
    storage_key: str
    """Object-store key locating the uploaded bytes."""


class DocumentStatusChangedEvent(DomainEvent):
    """Published when document processing status changes."""

    event_type: Literal[EventType.DOCUMENT_STATUS_CHANGED] = EventType.DOCUMENT_STATUS_CHANGED  # type: ignore[assignment]
    """Discriminator fixing this envelope to the document.status_changed event type."""
    document_id: str
    """Identifier of the document whose status changed."""
    workspace_id: str
    """Identifier of the workspace the document belongs to."""
    old_status: str
    """Processing status the document transitioned from."""
    new_status: str
    """Processing status the document transitioned to."""
    error: str = ""
    """Failure detail when the new status is an error state, empty otherwise."""


class DocumentDeletedEvent(DomainEvent):
    """Published when a document is deleted."""

    event_type: Literal[EventType.DOCUMENT_DELETED] = EventType.DOCUMENT_DELETED  # type: ignore[assignment]
    """Discriminator fixing this envelope to the document.deleted event type."""
    document_id: str
    """Identifier of the deleted document."""
    workspace_id: str
    """Identifier of the workspace the document belonged to."""


# --- Team Events ---


class TeamCreatedEvent(DomainEvent):
    """Published when a team is created within an organization."""

    event_type: Literal[EventType.TEAM_CREATED] = EventType.TEAM_CREATED  # type: ignore[assignment]
    """Discriminator fixing this envelope to the team.created event type."""
    team_id: str
    """Identifier of the newly created team."""
    team_name: str
    """Human-readable name of the created team."""


class TeamMemberAddedEvent(DomainEvent):
    """Published when a member is added to a team."""

    event_type: Literal[EventType.TEAM_MEMBER_ADDED] = EventType.TEAM_MEMBER_ADDED  # type: ignore[assignment]
    """Discriminator fixing this envelope to the team.member_added event type."""
    team_id: str
    """Identifier of the team the member was added to."""
    member_id: str
    """Identifier of the member added to the team."""
    role: str
    """Role granted to the member within the team."""


# --- Workspace Events ---


class WorkspaceTeamGrantedEvent(DomainEvent):
    """Published when a team is granted access to a workspace."""

    event_type: Literal[EventType.WORKSPACE_TEAM_GRANTED] = EventType.WORKSPACE_TEAM_GRANTED  # type: ignore[assignment]
    """Discriminator fixing this envelope to the workspace.team_granted event type."""
    workspace_id: str
    """Identifier of the workspace access was granted on."""
    team_id: str
    """Identifier of the team granted access."""
    permission: str
    """Permission level granted to the team on the workspace."""


# --- Organization Events ---


class OrganizationMemberAddedEvent(DomainEvent):
    """Published when a member joins an organization."""

    event_type: Literal[EventType.ORGANIZATION_MEMBER_ADDED] = EventType.ORGANIZATION_MEMBER_ADDED  # type: ignore[assignment]
    """Discriminator fixing this envelope to the organization.member_added event type."""
    member_id: str
    """Identifier of the member added to the organization."""
    email: str
    """Email address of the added member."""
    role: str
    """Role granted to the member within the organization."""


# --- Connector Events ---


class ConnectorSyncCompletedEvent(DomainEvent):
    """Published when a connector finishes syncing documents."""

    event_type: Literal[EventType.CONNECTOR_SYNC_COMPLETED] = EventType.CONNECTOR_SYNC_COMPLETED  # type: ignore[assignment]
    """Discriminator fixing this envelope to the connector.sync_completed event type."""
    connector_id: str
    """Identifier of the connector that completed the sync."""
    workspace_id: str
    """Identifier of the workspace the connector syncs into."""
    documents_new: int
    """Count of documents newly added during the sync."""
    documents_updated: int
    """Count of documents updated during the sync."""
    documents_deleted: int
    """Count of documents deleted during the sync."""
    errors: int
    """Count of errors encountered during the sync run."""


# --- Event Parsing ---

# Union type for all events (used by parse_event)
AnyEvent = Annotated[
    DocumentUploadedEvent
    | DocumentStatusChangedEvent
    | DocumentDeletedEvent
    | TeamCreatedEvent
    | TeamMemberAddedEvent
    | WorkspaceTeamGrantedEvent
    | OrganizationMemberAddedEvent
    | ConnectorSyncCompletedEvent,
    Field(discriminator="event_type"),
]

# Registry for dispatching by event_type
_EVENT_REGISTRY: dict[EventType, type[DomainEvent]] = {
    EventType.DOCUMENT_UPLOADED: DocumentUploadedEvent,
    EventType.DOCUMENT_STATUS_CHANGED: DocumentStatusChangedEvent,
    EventType.DOCUMENT_DELETED: DocumentDeletedEvent,
    EventType.TEAM_CREATED: TeamCreatedEvent,
    EventType.TEAM_MEMBER_ADDED: TeamMemberAddedEvent,
    EventType.WORKSPACE_TEAM_GRANTED: WorkspaceTeamGrantedEvent,
    EventType.ORGANIZATION_MEMBER_ADDED: OrganizationMemberAddedEvent,
    EventType.CONNECTOR_SYNC_COMPLETED: ConnectorSyncCompletedEvent,
}
"""Maps each EventType to its concrete DomainEvent model for parse_event dispatch."""


def parse_event(data: str | bytes | dict) -> DomainEvent:
    """Parse a JSON event payload and return the concrete event type.

    Dispatches to the correct Pydantic model based on the event_type field.

    Args:
        data: JSON string, bytes, or pre-parsed dict.

    Returns:
        The concrete DomainEvent subclass instance.

    Raises:
        ValueError: If event_type is unknown or data is invalid.

    """
    parsed = json.loads(data) if isinstance(data, (str, bytes)) else data

    event_type_str = parsed.get("event_type")
    if not event_type_str:
        msg = "Missing event_type field"
        raise ValueError(msg)

    try:
        event_type = EventType(event_type_str)
    except ValueError:
        msg = f"Unknown event type: {event_type_str}"
        raise ValueError(msg) from None

    event_class = _EVENT_REGISTRY.get(event_type)
    if event_class is None:
        msg = f"No handler registered for event type: {event_type_str}"
        raise ValueError(msg)

    return event_class.model_validate(parsed)
