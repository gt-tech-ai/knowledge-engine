"""Vector-backed RetrievalEngine (local dev): embed the query, then similarity-search a VectorStore.

Composes an ``EmbeddingProvider`` (Ollama) + a ``VectorStore`` (Qdrant). Only the request filters
named in ``filter_keys`` (payload fields, e.g. a tenant scope) are pushed to the store; every other
filter is enforced by the ``FilteringRetrievalEngine`` decorator. Each hit is mapped into a
``RetrievalResult`` preserving every metadata key, so the decorator's policies see them.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.clients.vector.composition import require_matching_dimension
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.vector_store import VectorSearchResult, VectorStore


class VectorRetrievalEngine(NoOpAsyncResource, RetrievalEngine):
    """RetrievalEngine over an EmbeddingProvider + VectorStore (embed → payload-filtered search)."""

    def __init__(
        self,
        embedder: EmbeddingProvider,
        store: VectorStore,
        collection: str,
        *,
        filter_keys: Sequence[str] = (),
    ) -> None:
        """Compose the embedder + store; fail loudly if their vector dimensions disagree.

        ``filter_keys`` names the request filters pushed down to the store as payload filters.
        """
        require_matching_dimension(embedder, store, collection)
        self._embedder = embedder
        self._store = store
        self._collection = collection
        self._filter_keys = tuple(filter_keys)

    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,  # noqa: ARG002 — one collection; no per-call index routing
    ) -> list[RetrievalResult]:
        """Embed the query, search with the pushed-down filters, and map hits to RetrievalResults."""
        request = filters or {}
        embedding = await self._embedder.embed(query)
        hits = await self._store.search(
            self._collection,
            embedding.embedding,
            top_k=top_k,
            filters={k: request[k] for k in self._filter_keys if k in request},
        )
        return [_to_retrieval_result(hit) for hit in hits]


def _to_retrieval_result(hit: VectorSearchResult) -> RetrievalResult:
    """Map a VectorSearchResult into a RetrievalResult, preserving the full metadata payload.

    ``hit.metadata`` is a freshly-built, owned dict (``qdrant/store.py`` builds one per hit) and ``hit``
    is discarded right after this mapping, so the result carries it directly rather than copying it.
    """
    page_raw = hit.metadata.get("page_number", "")
    page_number = int(page_raw) if page_raw.isdigit() else None
    return RetrievalResult(
        document_id=hit.document_id,
        document_name=hit.metadata.get("document_name", ""),
        chunk_content=hit.content,
        score=hit.score,
        page_number=page_number,
        metadata=hit.metadata,
    )
