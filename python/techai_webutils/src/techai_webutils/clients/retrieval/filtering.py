"""Server-side re-validating retrieval decorator (defense-in-depth).

Wraps any ``RetrievalEngine`` and drops passages that fail re-validation — regardless of the
vector store's own metadata filter — so a mislabeled or cross-workspace passage cannot leak:

- **workspace isolation**: the passage's ``workspace_id`` must equal the request's.
- **classification vs clearance**: the passage's classification must be <= the caller's
  clearance (from ``filters['clearance_level']``); unknown/absent classification is treated as
  the most restricted (fail closed).
- **relevance threshold**: passages below ``min_score`` are excluded.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.retrieval import RetrievalEngine
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.retrieval import RetrievalResult

logger = get_logger(__name__)

_CLASSIFICATION_RANK = {"public": 0, "internal": 1, "confidential": 2, "restricted": 3}
"""Ordered classification sensitivity ranks; a caller sees documents at or below their clearance."""
_MOST_RESTRICTED_RANK = len(_CLASSIFICATION_RANK)
"""Rank for an unknown/absent classification — the most restricted (fail closed)."""
_DEFAULT_MIN_SCORE = 0.5
"""Default relevance-score floor below which passages are excluded."""

CLEARANCE_FILTER_KEY = "clearance_level"
"""Retrieval-request filter key carrying the caller's clearance (shared with the query workflow producer)."""


def _rank(classification: str) -> int:
    """Return the sensitivity rank; unknown/blank classifications are most restricted (fail closed)."""
    return _CLASSIFICATION_RANK.get(classification, _MOST_RESTRICTED_RANK)


def _clearance_rank(clearance: str) -> int:
    """Return the caller's clearance rank; unknown/blank clearance is least-privileged (public — fail closed).

    Note the deliberate asymmetry with ``_rank``: an unknown *document classification* must be the MOST
    restricted (denied to everyone), but an unknown *caller clearance* must be the LEAST privileged (sees
    only public). Both directions fail closed. Reusing ``_rank``'s most-restricted default for the
    clearance ceiling would instead fail OPEN — a bad/unrecognized clearance value would clear everything.
    """
    return _CLASSIFICATION_RANK.get(clearance, 0)


def allowed_classifications_for(clearance: str) -> list[str]:
    """Return the classification labels at/below ``clearance`` — the KB ``in``-filter value list.

    Mirrors ``_allowed``'s ranking EXACTLY (a document is admissible iff its classification rank is
    <= the caller's clearance rank), so the KB pre-filter returns the SAME eligible set the
    ``FilteringRetrievalEngine`` decorator would keep — the two cannot drift because they share this
    one table (as they already share ``CLEARANCE_FILTER_KEY``). The decorator stays as
    defense-in-depth (it also fail-closes on an unlabeled document the KB filter can't express).
    """
    ceiling = _clearance_rank(clearance)
    return [label for label, rank in _CLASSIFICATION_RANK.items() if rank <= ceiling]


class FilteringRetrievalEngine(DelegatingAsyncResource[RetrievalEngine], RetrievalEngine):
    """RetrievalEngine decorator enforcing workspace + clearance isolation and a score floor."""

    def __init__(self, inner: RetrievalEngine, *, min_score: float = _DEFAULT_MIN_SCORE) -> None:
        """Wrap ``inner`` and set the minimum relevance score."""
        self._inner = inner
        self._min_score = min_score

    async def retrieve(
        self,
        query: str,
        workspace_id: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        knowledge_base_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve from the inner engine, then re-validate every passage server-side.

        ``knowledge_base_id`` (per-org routing) is forwarded to the inner engine unchanged;
        the server-side workspace + clearance re-validation below is unaffected by which KB was queried.
        """
        results = await self._inner.retrieve(query, workspace_id, top_k, filters, knowledge_base_id)
        clearance_rank = _clearance_rank((filters or {}).get(CLEARANCE_FILTER_KEY, "public"))
        kept = [r for r in results if self._allowed(r, workspace_id, clearance_rank)]
        # Observability: a 0-result response is otherwise indistinguishable from an error. Logging the
        # retrieved-vs-kept counts + floor makes "nothing retrieved" vs "all filtered out" diagnosable.
        logger.info(
            "retrieval passages filtered",
            workspace_id=workspace_id,
            retrieved=len(results),
            kept=len(kept),
            dropped=len(results) - len(kept),
            min_score=self._min_score,
        )
        return kept

    def _allowed(self, result: RetrievalResult, workspace_id: str, clearance_rank: int) -> bool:
        """Return True iff the passage passes the score floor + workspace + clearance checks.

        A passage with no (or blank) ``classification`` is treated as most-restricted and denied to
        everyone — fail closed — so an unlabeled document never leaks before it is classified.
        """
        if result.score < self._min_score:
            return False
        if result.metadata.get("workspace_id") != workspace_id:
            return False
        return _rank(result.metadata.get("classification", "")) <= clearance_rank
