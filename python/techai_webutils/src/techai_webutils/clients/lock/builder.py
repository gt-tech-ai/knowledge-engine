"""Env-aware single-writer lock factory (the foundation/logger ``NewFromConfig`` pattern).

Selects the in-process ``InMemoryLock`` (dev / replica=1) or the cross-pod ``PostgresAdvisoryLock``
(stage/prod) from config (``LockConfig.kind``). Unknown kinds fail loudly. The Postgres backend and
its ``asyncpg`` dependency are imported lazily so the dev/memory path never loads asyncpg.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING
from urllib.parse import quote

from techai_webutils.clients.lock.memory import InMemoryLock

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.lock import ManagedLock


class LockKind(StrEnum):
    """Which ``DistributedLock`` backend to build."""

    MEMORY = "memory"
    """In-process lock (dev / tests / single replica); no external dependency."""
    POSTGRES = "postgres"
    """Cross-pod Postgres advisory lock (stage/prod; needs the ``techai-webutils[postgres]`` extra)."""


@dataclass(frozen=True, slots=True)
class LockConfig:
    """Single-writer lock configuration resolved from ``ingestion.lock.*`` + the DB + KB settings."""

    kind: LockKind = LockKind.MEMORY
    """Selects the backend (``memory`` in dev / replica=1, ``postgres`` in stage/prod)."""
    host: str = "localhost"
    """Postgres host (unused by the memory backend)."""
    port: int = 5432
    """Postgres port."""
    user: str = ""
    """Postgres user (callers project it from DatabaseSettings; unused by the memory backend)."""
    password: str = ""
    """Postgres password."""
    database: str = ""
    """Postgres database name (callers project it from DatabaseSettings)."""
    sslmode: str = "disable"
    """libpq sslmode for the lock connection (``disable`` in dev; ``require``/``verify-*`` on RDS)."""
    knowledge_base_id: str = ""
    """Bedrock Knowledge Base id — half of the advisory-lock key."""
    data_source_id: str = ""
    """Bedrock data source id — the other half of the advisory-lock key."""


def _postgres_dsn(config: LockConfig) -> str:
    """Build a libpq DSN URI for the lock connection.

    asyncpg has no ``sslmode`` kwarg but parses it from the DSN URI, so TLS travels in the query
    string; credentials are percent-encoded to survive special characters.
    """
    user = quote(config.user, safe="")
    password = quote(config.password, safe="")
    database = quote(config.database, safe="")
    return f"postgresql://{user}:{password}@{config.host}:{config.port}/{database}?sslmode={config.sslmode}"


def new_lock_from_config(config: LockConfig) -> ManagedLock:
    """Build the ``DistributedLock`` backend selected by ``config.kind`` (memory or postgres)."""
    if config.kind is LockKind.MEMORY:
        return InMemoryLock()
    if config.kind is LockKind.POSTGRES:
        # Lazy import: keep asyncpg off the dev/memory path (loaded only in stage/prod).
        from techai_webutils.clients.lock.postgres import PostgresAdvisoryLock  # noqa: PLC0415

        return PostgresAdvisoryLock(
            dsn=_postgres_dsn(config),
            knowledge_base_id=config.knowledge_base_id,
            data_source_id=config.data_source_id,
        )
    msg = f"unknown lock kind: {config.kind!r}"
    raise ValueError(msg)
