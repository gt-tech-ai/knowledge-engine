"""Integration tests for PostgresAdvisoryLock against a real Postgres (testcontainer).

Unit tests drive the lock against a mocked asyncpg connection; these prove the two properties that
only real Postgres can demonstrate: cross-session mutual exclusion (one holder blocks contenders and
a ``SingleWriterRunner`` skips) and the crash-safe, no-TTL auto-release when the holding session is
dropped. Postgres image + credentials mirror the platform (docker-compose ``postgres:16-alpine``).
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.lock import SingleWriterRunner
from techai_webutils.clients.lock.postgres import PostgresAdvisoryLock


def _pair(dsn: str, key: str) -> tuple[PostgresAdvisoryLock, PostgresAdvisoryLock]:
    """Build two locks over separate sessions that contend on the same KB key."""
    return (
        PostgresAdvisoryLock(dsn=dsn, knowledge_base_id=key, data_source_id=key),
        PostgresAdvisoryLock(dsn=dsn, knowledge_base_id=key, data_source_id=key),
    )


@pytest.mark.integration
@pytest.mark.asyncio
async def test_single_writer_runner_serializes_across_sessions(postgres_dsn: str) -> None:
    """Test one holder blocks a contender and a SingleWriterRunner skips while the lock is held.

    **Why this test is important:**
      - This is the invariant the whole ADR exists for: at replica>1, exactly one pod may drive the
        Bedrock ingestion job. Two live sessions on one DB must not both acquire the same advisory lock.

    **What it tests:**
      - While session A holds the lock, a SingleWriterRunner over session B returns None without running
        the op; once A releases, B's runner runs.
    """
    a, b = _pair(postgres_dsn, "kb-serialize")
    async with a, b:
        ran: list[str] = []

        async def op() -> str:
            ran.append("b")
            return "b"

        assert await a.acquire() is True
        assert await SingleWriterRunner(op, b).run() is None  # B skips while A holds
        assert ran == []

        await a.release()
        assert await SingleWriterRunner(op, b).run() == "b"  # A released → B runs
        assert ran == ["b"]
        await b.release()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_dropped_session_auto_releases_the_lock(postgres_dsn: str) -> None:
    """Test dropping the holding session auto-releases the advisory lock (crash-safe, no TTL).

    **Why this test is important:**
      - Session-scoped auto-release is the reason Postgres was chosen over a TTL lock: a crashed pod
        frees the lock for free, with no lease to size against the long, unbounded Bedrock poll.

    **What it tests:**
      - With A holding the lock, B is blocked; after A's connection is closed (a simulated crash, no
        explicit release), B acquires successfully.
    """
    a, b = _pair(postgres_dsn, "kb-crash")
    async with a, b:
        assert await a.acquire() is True
        assert await b.acquire() is False  # blocked while A holds
        await a.aclose()  # crash: drop A's session with the lock still held
        assert await b.acquire() is True  # Postgres auto-released it — B wins, no TTL
        await b.release()
