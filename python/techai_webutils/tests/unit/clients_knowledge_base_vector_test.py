"""Tests for the vector-backed dev KnowledgeBase (chunk indexer) + factory (mocked deps)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.vector_kb.builder import (
    KnowledgeBaseConfig,
    KnowledgeBaseKind,
    new_knowledge_base_from_config,
)
from techai_webutils.clients.vector_kb.vector import VectorKnowledgeBase
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.vector_store import VectorStore


def _embedder(dimension: int = 3) -> EmbeddingProvider:
    """Mock EmbeddingProvider: embed_batch returns one EmbeddingResult per input chunk."""
    embedder = MagicMock(spec=EmbeddingProvider)
    embedder.dimension.return_value = dimension
    embedder.embed_batch = AsyncMock(
        side_effect=lambda texts: [
            EmbeddingResult(embedding=[0.1, 0.2, 0.3], model="m", token_count=0) for _ in texts
        ],
    )
    return embedder


def _store(count: int = 0, dimension: int = 3, present: set[str] | None = None) -> VectorStore:
    """Mock VectorStore: upsert/delete_by_document + count_by_document returning `count`.

    ``existing_ids`` reports the checkpoint set (``present``, default empty) intersected with the
    queried ids, so a test can pre-declare which chunk ids are already indexed to drive the resume path.
    """
    already = present or set()
    store = MagicMock(spec=VectorStore)
    store.dimension = dimension
    store.upsert = AsyncMock()
    store.delete_by_document = AsyncMock()
    store.count_by_document = AsyncMock(return_value=count)
    store.existing_ids = AsyncMock(side_effect=lambda _collection, ids: {i for i in ids if i in already})
    return store


class TestVectorKnowledgeBase:
    @pytest.mark.asyncio
    async def test_index_document_embeds_and_upserts_with_metadata_schema(self) -> None:
        """index_document embeds each chunk and upserts points carrying the consumer's attributes.

        Why this test is important:
          - Retrieval pushes down and re-validates the consumer's scope keys; if index_document dropped
            the attributes, the passages would be indexed but every query would filter them out.

        What it tests:
          - embed_batch is called with the chunks; upsert writes one VectorEntry per chunk with the
            given attributes (tenant, level), the document_id and the chunk_index.
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, collection="documents")

        await kb.index_document(
            "doc-1", ["first chunk", "second chunk"], attributes={"tenant": "t1", "level": "mid"}
        )

        embedder.embed_batch.assert_awaited_once_with(["first chunk", "second chunk"])
        store.upsert.assert_awaited_once()
        entries = store.upsert.call_args.args[1]
        assert len(entries) == 2
        assert entries[0].id == "doc-1:0"
        assert entries[0].document_id == "doc-1"
        assert entries[0].content == "first chunk"
        assert entries[0].metadata["tenant"] == "t1"
        assert entries[0].metadata["level"] == "mid"
        assert entries[0].metadata["chunk_index"] == "0"
        # No document_name passed → falls back to the document_id.
        assert entries[0].metadata["document_name"] == "doc-1"

    @pytest.mark.asyncio
    async def test_index_document_stamps_document_name_for_citation(self) -> None:
        """A passed document_name is stamped onto every chunk so a citation can display the filename.

        Why this test is important:
          - The local-vector path has no native citations; the retrieval side reads ``document_name``
            from each chunk's metadata to show a human-readable source (e.g. ``moby-dick.txt``). Without
            threading the uploaded filename through, every citation shows the opaque document id.

        What it tests:
          - index_document(document_name="moby-dick.txt") writes that name onto each upserted chunk's
            metadata (not the document_id).
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")

        await kb.index_document("doc-1", ["a chunk"], document_name="moby-dick.txt")

        entries = store.upsert.call_args.args[1]
        assert entries[0].metadata["document_name"] == "moby-dick.txt"

    @pytest.mark.asyncio
    async def test_index_document_empty_document_name_falls_back_to_id(self) -> None:
        """An EXPLICIT empty document_name (an ingestion event with no file_name) falls back to the id.

        Why this test is important:
          - ``DocumentUploadedEvent.file_name`` defaults to "" when the upload message omits it
            (``events.parse_document_uploaded``), and the ingestion job threads that value straight
            through as ``document_name``. This pins the ``document_name or document_id`` fallback for the
            empty-string case (not just an omitted arg), so a citation never renders a blank source label.

        What it tests:
          - index_document(document_name="") stamps the document_id onto each chunk's ``document_name``.
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")

        await kb.index_document("doc-1", ["a chunk"], document_name="")

        entries = store.upsert.call_args.args[1]
        assert entries[0].metadata["document_name"] == "doc-1"

    @pytest.mark.asyncio
    async def test_index_document_empty_chunks_is_a_noop(self) -> None:
        """Indexing zero chunks embeds nothing and upserts nothing."""
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")
        await kb.index_document("doc-1", [])
        embedder.embed_batch.assert_not_awaited()
        store.upsert.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_index_document_upserts_incrementally_per_sub_batch(self) -> None:
        """A multi-sub-batch document embeds AND upserts one sub-batch at a time, not all-at-once.

        Why this test is important:
          - A large document (thousands of chunks) must not embed everything into memory and then do a
            single all-or-nothing upsert: on the CPU-Ollama path that one giant unit times out and the
            whole document is lost. Incremental per-sub-batch upsert bounds peak memory and — because
            each sub-batch is persisted as it completes — lets a redrive resume instead of restarting.

        What it tests:
          - 150 chunks (sub-batch size 96) drive TWO embed_batch calls and TWO upserts (96 then 54),
            and the emitted point ids span the whole document (doc-1:0 .. doc-1:149) with no gaps.
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")
        chunks = [f"chunk-{i}" for i in range(150)]

        await kb.index_document("doc-1", chunks)

        assert embedder.embed_batch.await_count == 2
        assert store.upsert.await_count == 2
        upserted_ids = [e.id for call in store.upsert.call_args_list for e in call.args[1]]
        assert upserted_ids == [f"doc-1:{i}" for i in range(150)]

    @pytest.mark.asyncio
    async def test_index_document_skips_already_indexed_sub_batches(self) -> None:
        """A redrive re-embeds ONLY the chunks not already in the store (checkpoint resume).

        Why this test is important:
          - Embedding is the expensive CPU step. When an at-least-once redrive (or a mid-ingest worker
            restart) re-runs a partially indexed document, re-embedding the sub-batches already upserted
            is wasted CPU that can keep a large document from ever converging. The stable point ids make
            a sub-batch skippable when every id in it is already present.

        What it tests:
          - With the first sub-batch (doc-1:0..95) already present, a 150-chunk re-index embeds and
            upserts ONLY the second sub-batch (the 54 remaining chunks), leaving the done work untouched.
        """
        embedder = _embedder()
        store = _store(present={f"doc-1:{i}" for i in range(96)})
        kb = VectorKnowledgeBase(embedder, store, "documents")
        chunks = [f"chunk-{i}" for i in range(150)]

        await kb.index_document("doc-1", chunks)

        embedder.embed_batch.assert_awaited_once_with(chunks[96:])
        store.upsert.assert_awaited_once()
        upserted_ids = [e.id for e in store.upsert.call_args.args[1]]
        assert upserted_ids == [f"doc-1:{i}" for i in range(96, 150)]

    @pytest.mark.asyncio
    async def test_get_document_status_ready_when_points_exist_else_none(self) -> None:
        """get_document_status reports ready+chunk_count when points exist, None when the doc is absent.

        Why this test is important:
          - The ingest/status API surfaces this; a wrong "ready" on an empty doc, or None on an indexed
            doc, misreports ingestion state to the UI.

        What it tests:
          - count>0 -> KBDocument(status="ready", chunk_count=count); count==0 -> None.
        """
        embedder = _embedder()
        ready = VectorKnowledgeBase(embedder, _store(count=5), "documents")
        doc = await ready.get_document_status("doc-1")
        assert doc is not None
        assert doc.status == "ready"
        assert doc.chunk_count == 5

        missing = VectorKnowledgeBase(embedder, _store(count=0), "documents")
        assert await missing.get_document_status("absent") is None

    @pytest.mark.asyncio
    async def test_remove_document_deletes_by_document(self) -> None:
        """remove_document deletes every chunk of the document from the store."""
        store = _store()
        kb = VectorKnowledgeBase(_embedder(), store, "documents")
        await kb.remove_document("doc-1")
        store.delete_by_document.assert_awaited_once_with("documents", "doc-1")

    def test_dimension_mismatch_fails_loudly(self) -> None:
        """A drifted embedder/store dimension raises at construction (single source of truth)."""
        with pytest.raises(ValueError, match="dimension"):
            VectorKnowledgeBase(_embedder(dimension=768), _store(dimension=3), "documents")


class TestKnowledgeBaseFactory:
    def test_from_config_builds_vector_kb(self) -> None:
        """new_knowledge_base_from_config(kind=vector) builds a VectorKnowledgeBase."""
        kb = new_knowledge_base_from_config(
            KnowledgeBaseConfig(
                kind=KnowledgeBaseKind.VECTOR,
                vector_url="http://qdrant:6333",
                collection="documents",
                embedding_host="http://ollama:11434",
                embedding_dimension=768,
            ),
        )
        assert isinstance(kb, VectorKnowledgeBase)

    def test_unknown_kind_raises(self) -> None:
        """An unknown knowledge-base kind fails loudly."""
        with pytest.raises(ValueError, match="unknown knowledge base kind"):
            new_knowledge_base_from_config(KnowledgeBaseConfig(kind="bogus"))  # type: ignore[arg-type]
