"""Vector-backed KnowledgeBase (local dev chunk indexer): embed chunks → upsert into a VectorStore.

This is the dev analogue of the Bedrock Knowledge Base: instead of handing documents to a managed
service, it embeds each chunk (Ollama) and upserts it into a VectorStore (Qdrant), writing the
retrieval metadata (``workspace_id``, ``document_id``, ``chunk_index``, and a ``public`` classification
so the ``FilteringRetrievalEngine`` admits the passage). It composes the same ``VectorStore`` the
retrieval engine reads, so an indexed document is immediately queryable end-to-end.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.clients.vector.composition import require_matching_dimension
from techai_webutils.core.interfaces.knowledge_base import (
    KBDocument,
    KBSyncResult,
    KnowledgeBase,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.core.interfaces.vector_store import VectorEntry
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.vector_store import VectorStore

logger = get_logger(__name__)

# Fallback classification when the caller supplies none (e.g. the dev-index tool over synthetic
# public-domain prose): treat the chunk as public so the security filter admits it at the lowest
# clearance. The real ingestion path threads the document's actual classification through
# index_document, so a confidential document is stored (and filtered) as confidential, not public.
_DEV_CLASSIFICATION = "public"
"""Fallback chunk classification when the caller supplies none (public — admitted at lowest clearance)."""

# Embed + upsert one document a fixed-size sub-batch at a time, instead of embedding EVERY
# chunk up front and doing one all-or-nothing upsert — on the CPU-Ollama path that single giant unit
# exceeds the embed read timeout and the whole (thousands-of-chunk) document is lost and redriven from
# zero forever. Sub-batches are processed SEQUENTIALLY, not concurrently: this KnowledgeBase is only the
# local Ollama+Qdrant path (Bedrock uses a different implementation), and Ollama is a single CPU-bound
# backend, so concurrent embed calls do not parallelize — they make each HTTP request wait behind the
# others and blow past its read timeout. One in-flight embed gives each call the whole backend.
_EMBED_SUB_BATCH = 96
"""Number of chunks embedded + upserted per sub-batch, bounding a document's per-unit embed work."""


class VectorKnowledgeBase(NoOpAsyncResource, KnowledgeBase):
    """KnowledgeBase over an EmbeddingProvider + VectorStore (embed chunks → upsert; count for status)."""

    def __init__(self, embedder: EmbeddingProvider, store: VectorStore, collection: str) -> None:
        """Compose the embedder + store; fail loudly if their vector dimensions disagree."""
        require_matching_dimension(embedder, store, collection)
        self._embedder = embedder
        self._store = store
        self._collection = collection

    async def index_document(
        self,
        workspace_id: str,
        document_id: str,
        chunks: list[str],
        classification: str = _DEV_CLASSIFICATION,
        document_name: str = "",
    ) -> None:
        """Embed + upsert one point per chunk, a sub-batch at a time, resuming past indexed sub-batches.

        Each sub-batch is embedded and upserted before the next, so peak memory is O(sub-batch) not
        O(document) and every completed sub-batch is durable. Before embedding a sub-batch its stable
        ``{document_id}:{index}`` ids are probed against the store: a sub-batch already fully present is
        skipped (checkpoint resume), so an at-least-once redrive — or a mid-ingest worker restart —
        re-embeds only the sub-batches not yet fully present instead of redoing the expensive CPU
        embedding from zero. A partially-present sub-batch is re-embedded and re-upserted (idempotent),
        so resume is exact. ``classification`` is stamped onto every chunk for the clearance filter;
        an empty value falls back to public (the lowest bar). ``document_name`` (the uploaded filename)
        is stamped onto every chunk for citation display, falling back to ``document_id`` when empty.
        """
        if not chunks:
            return
        classification = classification or _DEV_CLASSIFICATION
        sub_batches = [chunks[i : i + _EMBED_SUB_BATCH] for i in range(0, len(chunks), _EMBED_SUB_BATCH)]
        total = len(sub_batches)
        logger.info("vector index: start", document_id=document_id, chunks=len(chunks), sub_batches=total)
        base = 0
        skipped = 0
        for number, batch in enumerate(sub_batches, start=1):
            ids = [f"{document_id}:{base + offset}" for offset in range(len(batch))]
            if await self._store.existing_ids(self._collection, ids) == set(ids):
                base += len(batch)
                skipped += 1
                continue
            embeddings = await self._embedder.embed_batch(batch)
            # The positional zip below assumes one embedding per chunk; a provider that returns a short
            # (or over-long) list would otherwise IndexError mid-loop or silently drop chunks. Fail fast
            # and loud so a partial embed is a clear error, not a corrupt index.
            if len(embeddings) != len(batch):
                msg = f"embedder returned {len(embeddings)} embeddings for {len(batch)} chunks"
                raise ValueError(msg)
            entries = [
                VectorEntry(
                    id=ids[offset],
                    document_id=document_id,
                    content=batch[offset],
                    embedding=embeddings[offset].embedding,
                    metadata={
                        "workspace_id": workspace_id,
                        # Prefer the uploaded filename so a retrieval citation displays it; fall back to
                        # the (readable) document_id when a caller passes no name — e.g. the dev-index
                        # tool, whose document_id already is the filename.
                        "document_name": document_name or document_id,
                        "chunk_index": str(base + offset),
                        "classification": classification,
                    },
                )
                for offset in range(len(batch))
            ]
            await self._store.upsert(self._collection, entries)
            base += len(batch)
            logger.info("vector index: sub-batch upserted", document_id=document_id, done=number, total=total)
        logger.info("vector index: done", document_id=document_id, points=len(chunks), skipped=skipped)

    async def remove_document(self, workspace_id: str, document_id: str) -> None:  # noqa: ARG002
        """Remove every chunk of the document from the store (document ids are unique in dev)."""
        await self._store.delete_by_document(self._collection, document_id)

    async def get_document_status(self, workspace_id: str, document_id: str) -> KBDocument | None:
        """Report ``ready`` + the chunk count if the document has points, else ``None`` (not indexed)."""
        count = await self._store.count_by_document(self._collection, document_id)
        if count == 0:
            return None
        return KBDocument(
            id=document_id,
            workspace_id=workspace_id,
            file_name=document_id,
            content_type="text/plain",
            status="ready",
            chunk_count=count,
        )

    async def sync(self, workspace_id: str) -> KBSyncResult:  # noqa: ARG002
        """No-op sync: upserts are synchronous in the vector store, so there is nothing to reconcile."""
        return KBSyncResult(
            documents_added=0,
            documents_updated=0,
            documents_removed=0,
            chunks_created=0,
            errors=[],
        )
