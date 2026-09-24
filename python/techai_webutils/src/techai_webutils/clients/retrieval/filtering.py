"""Server-side re-validating retrieval decorator (defense-in-depth).

Wraps any ``RetrievalEngine`` and drops passages that fail its ``PassagePolicy`` rules — regardless
of the vector store's own metadata filter — so a mislabeled or out-of-scope passage cannot leak. A
consumer supplies its own policies (``MinScore``, ``MetadataEquals``, ``OrdinalCeiling`` or its own);
without them the default rules apply:

- **workspace isolation**: the passage's ``workspace_id`` must equal the request's.
- **classification vs clearance**: the passage's classification must be <= the caller's
  clearance (from ``filters['clearance_level']``); unknown/absent classification is treated as
  the most restricted (fail closed).
- **relevance threshold**: passages below ``min_score`` are excluded.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Protocol

from techai_webutils.core.interfaces.retrieval import RetrievalEngine
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.retrieval import RetrievalResult

logger = get_logger(__name__)

_CLASSIFICATION_RANK = {"public": 0, "internal": 1, "confidential": 2, "restricted": 3}
"""Ordered classification sensitivity ranks; a caller sees documents at or below their clearance."""
_DEFAULT_MIN_SCORE = 0.5
"""Default relevance-score floor below which passages are excluded."""

CLEARANCE_FILTER_KEY = "clearance_level"
"""Retrieval-request filter key carrying the caller's clearance (shared with the query workflow producer)."""


class PassagePolicy(Protocol):
    """A rule every retrieved passage must pass to reach the caller."""

    def admits(self, passage: RetrievalResult, request: Mapping[str, str]) -> bool:
        """Return True iff ``passage`` may be returned for ``request``.

        ``request`` is the call's filters plus its ``workspace_id``.
        """
        ...


@dataclass(frozen=True, slots=True)
class MinScore:
    """Admits passages whose relevance score is at least ``floor``."""

    floor: float
    """The lowest admitted score."""

    def admits(self, passage: RetrievalResult, request: Mapping[str, str]) -> bool:  # noqa: ARG002
        """Return True iff the passage scores at or above the floor."""
        return passage.score >= self.floor


@dataclass(frozen=True, slots=True)
class MetadataEquals:
    """Admits passages whose metadata ``key`` equals the request's ``request_key`` (default: ``key``).

    A request without the key admits nothing (fail closed), so a missing scope never widens results.
    """

    key: str
    """The passage metadata key compared."""
    request_key: str = ""
    """The request key holding the required value; empty means ``key``."""

    def admits(self, passage: RetrievalResult, request: Mapping[str, str]) -> bool:
        """Return True iff the passage's metadata value equals the request's."""
        wanted = request.get(self.request_key or self.key)
        return wanted is not None and passage.metadata.get(self.key) == wanted


@dataclass(frozen=True, slots=True)
class OrdinalCeiling:
    """Admits passages whose ``key`` label ranks at or below the caller's ``request_key`` label.

    ``ranks`` orders the labels from least to most sensitive. Both directions fail closed: an unknown
    or missing passage label ranks above every level (denied to everyone), and an unknown or missing
    caller level ranks as the lowest.
    """

    key: str
    """The passage metadata key holding its label."""
    request_key: str
    """The request key holding the caller's level."""
    ranks: tuple[str, ...]
    """The labels in increasing sensitivity."""
    _order: dict[str, int] = field(init=False, repr=False, compare=False)
    """Label → rank, built once from ``ranks``."""

    def __post_init__(self) -> None:
        """Index ``ranks`` once so ``admits`` is a dict lookup per passage."""
        object.__setattr__(self, "_order", {label: i for i, label in enumerate(self.ranks)})

    def admits(self, passage: RetrievalResult, request: Mapping[str, str]) -> bool:
        """Return True iff the passage's label ranks at or below the caller's level."""
        passage_rank = self._order.get(passage.metadata.get(self.key, ""), len(self.ranks))
        return passage_rank <= self._order.get(request.get(self.request_key, ""), 0)


def default_policies(min_score: float = _DEFAULT_MIN_SCORE) -> list[PassagePolicy]:
    """Return the default rules: the score floor, workspace isolation, classification vs clearance."""
    return [
        MinScore(min_score),
        MetadataEquals("workspace_id"),
        OrdinalCeiling("classification", CLEARANCE_FILTER_KEY, tuple(_CLASSIFICATION_RANK)),
    ]


def _clearance_rank(clearance: str) -> int:
    """Return the caller's clearance rank; unknown/blank clearance is least-privileged (public — fail closed).

    Note the deliberate asymmetry with the passage side (``OrdinalCeiling``): an unknown *document
    classification* must be the MOST restricted (denied to everyone), but an unknown *caller clearance*
    must be the LEAST privileged (sees only public). Both directions fail closed. Using the
    most-restricted default for the clearance ceiling would instead fail OPEN — a bad/unrecognized
    clearance value would clear everything.
    """
    return _CLASSIFICATION_RANK.get(clearance, 0)


def allowed_classifications_for(clearance: str) -> list[str]:
    """Return the classification labels at/below ``clearance`` — the KB ``in``-filter value list.

    Mirrors the default ``OrdinalCeiling`` ranking EXACTLY (a document is admissible iff its
    classification rank is <= the caller's clearance rank), so the KB pre-filter returns the SAME
    eligible set the ``FilteringRetrievalEngine`` decorator would keep — the two cannot drift because they share this
    one table (as they already share ``CLEARANCE_FILTER_KEY``). The decorator stays as
    defense-in-depth (it also fail-closes on an unlabeled document the KB filter can't express).
    """
    ceiling = _clearance_rank(clearance)
    return [label for label, rank in _CLASSIFICATION_RANK.items() if rank <= ceiling]


class FilteringRetrievalEngine(DelegatingAsyncResource[RetrievalEngine], RetrievalEngine):
    """RetrievalEngine decorator that keeps only the passages every policy admits."""

    def __init__(
        self,
        inner: RetrievalEngine,
        *,
        min_score: float = _DEFAULT_MIN_SCORE,
        policies: Sequence[PassagePolicy] | None = None,
    ) -> None:
        """Wrap ``inner`` with ``policies``; ``None`` uses ``default_policies(min_score)``."""
        self._inner = inner
        self._min_score = min_score
        self._policies = list(policies) if policies is not None else default_policies(min_score)

    async def retrieve(
        self,
        query: str,
        workspace_id: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        knowledge_base_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve from the inner engine, then re-validate every passage server-side.

        ``knowledge_base_id`` (per-index routing) is forwarded to the inner engine unchanged; the
        policies see the call's filters plus its ``workspace_id``, whichever index was queried.
        """
        results = await self._inner.retrieve(query, workspace_id, top_k, filters, knowledge_base_id)
        request = {**(filters or {}), "workspace_id": workspace_id}
        kept = [r for r in results if all(p.admits(r, request) for p in self._policies)]
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
