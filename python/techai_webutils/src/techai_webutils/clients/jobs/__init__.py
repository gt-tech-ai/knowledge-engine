"""Background job infrastructure.

Provides in-memory and database-backed implementations of the job interfaces
defined in ``core/interfaces/jobs.py``. Mirrors Go's ``pkg/go/clients/jobs/``
package backed by River.

Phase 1: In-memory implementations for local development and testing.
Phase 3: PostgreSQL-backed persistent job queue.
"""

from techai_webutils.clients.jobs.builder import new_jobs_from_config
from techai_webutils.clients.jobs.config import JobConfig, JobKind
from techai_webutils.clients.jobs.memory import (
    InMemoryJobEnqueuer,
    InMemoryJobScheduler,
    InMemoryWorkerRegistry,
)

__all__ = [
    "InMemoryJobEnqueuer",
    "InMemoryJobScheduler",
    "InMemoryWorkerRegistry",
    "JobConfig",
    "JobKind",
    "new_jobs_from_config",
]
