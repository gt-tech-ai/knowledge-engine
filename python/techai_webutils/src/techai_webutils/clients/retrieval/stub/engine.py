"""Deterministic in-memory retrieval engine for local development and tests.

Returns the passages it was constructed with (none by default), capped at ``top_k``, whatever the
query — so a consumer can run its retrieval path without a real index by supplying its own sample
corpus. Scope rules still apply through ``FilteringRetrievalEngine``.
"""

from __future__ import annotations

from dataclasses import replace
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence


class StubRetrievalEngine(NoOpAsyncResource, RetrievalEngine):
    """RetrievalEngine returning a fixed corpus (local development and tests)."""

    def __init__(self, passages: Sequence[RetrievalResult] = ()) -> None:
        """Hold ``passages`` as the corpus every query returns (empty by default)."""
        self._passages = tuple(passages)

    async def retrieve(
        self,
        query: str,  # noqa: ARG002
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,  # noqa: ARG002 - scope is enforced by the decorator
        index_id: str | None = None,  # noqa: ARG002 - one corpus regardless of index
    ) -> list[RetrievalResult]:
        """Return copies of the corpus passages (with their own metadata dicts), capped at ``top_k``."""
        return [replace(p, metadata=dict(p.metadata)) for p in self._passages[:top_k]]
