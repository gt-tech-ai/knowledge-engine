"""Ingestion job-state port — the API ``InternalService`` persisted-job read/write surface (audit F4).

The ingestion worker persists the CURRENT Bedrock ingestion job for a ``(knowledge_base_id,
data_source_id)`` pair so a restart or poll-timeout can *reattach* to an in-flight job instead of
racing a second ``StartIngestionJob`` (Bedrock serializes ingestion; a second start while one is
running returns ``ConflictException``). The concrete gRPC implementation is proto-bound and lives with
its consumer — the ingestion worker — injected at the composition root; this core module defines only
the proto-free Protocol. The API owns the row (Ent + InternalService).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from datetime import datetime

# The persisted job-state vocabulary — the exact string values the Go API stores (domain.IngestionJobState:
# JobRunning/JobComplete/JobFailed/JobStopping), so a state round-trips worker → InternalService → DB
# unchanged. RUNNING/STOPPING are non-terminal (a job still holds Bedrock's ingestion lock);
# COMPLETE/FAILED are terminal. Kept as strings (not a new enum) to avoid colliding with the Bedrock
# ``IngestionJobState`` StrEnum in ``core.interfaces.kb_ingestion`` (that models Bedrock's status;
# this models our persisted lifecycle).
RUNNING_STATE = "running"
"""Non-terminal: a job is in flight and still holds Bedrock's ingestion lock."""
COMPLETE_STATE = "complete"
"""Terminal: the job finished successfully."""
FAILED_STATE = "failed"
"""Terminal: the job failed (or was mapped from an unknown status / poll timeout)."""
STOPPING_STATE = "stopping"
"""Non-terminal: a stop was requested but the job is still winding down (lock still held)."""


@dataclass(frozen=True, slots=True)
class IngestionJobStateRecord:
    """The persisted job-state row for one KB data source — the read side of ``IngestionJobStateStore``.

    Returned by ``get`` when a row exists (``None`` when the data source is idle). ``started_at`` is
    when the current job was first recorded; the 32.4 watchdog compares it against ``datetime.now(UTC)``
    to detect a stuck job, so both timestamps are timezone-aware UTC.
    """

    knowledge_base_id: str
    """The Bedrock knowledge base this job-state row belongs to."""
    data_source_id: str
    """The KB's S3 data source this job-state row tracks."""
    job_id: str
    """The Bedrock ingestion job id to reattach to (empty when no job is in flight)."""
    state: str
    """One of the RUNNING/COMPLETE/FAILED/STOPPING state constants (running/complete/failed/stopping)."""
    started_at: datetime
    """When the current job was first recorded (tz-aware UTC)."""
    updated_at: datetime
    """When the state last transitioned (tz-aware UTC)."""


@runtime_checkable
class IngestionJobStateStore(Protocol):
    """Persists + reads the current Bedrock ingestion job for a KB data source via ``InternalService``.

    The app-owned complement of the single-writer lock: the lock coordinates *who* may start
    a job; this store records *which* job is in flight so a new lock holder reattaches instead of
    restarting. A single gRPC backend implements it; ``new_ingestion_job_state_from_config`` selects it.
    """

    async def get(self, knowledge_base_id: str, data_source_id: str) -> IngestionJobStateRecord | None:
        """Return the current job-state row for a KB data source, or ``None`` when the row is absent."""
        ...

    async def upsert(
        self,
        knowledge_base_id: str,
        data_source_id: str,
        *,
        job_id: str,
        state: str,
        failure_reason: str = "",
    ) -> None:
        """Record the current job + state for a KB data source (reason populated when ``state`` is failed)."""
        ...
