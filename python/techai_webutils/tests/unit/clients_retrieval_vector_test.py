"""Tests for the vector-backed dev RetrievalEngine + the qdrant factory branch (mocked deps)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.retrieval.filtering import FilteringRetrievalEngine
from techai_webutils.clients.retrieval.vector import VectorRetrievalEngine
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.vector_store import VectorSearchResult, VectorStore


def _embedder(dimension: int = 3) -> EmbeddingProvider:
    """Mock EmbeddingProvider returning a fixed vector of the given dimension."""
    embedder = MagicMock(spec=EmbeddingProvider)
    embedder.dimension.return_value = dimension
    embedder.embed = AsyncMock(
        return_value=EmbeddingResult(embedding=[0.1, 0.2, 0.3], model="m", token_count=0)
    )
    return embedder


def _store(hits: list[VectorSearchResult], dimension: int = 3) -> VectorStore:
    """Mock VectorStore returning fixed hits with a matching dimension."""
    store = MagicMock(spec=VectorStore)
    store.dimension = dimension
    store.search = AsyncMock(return_value=hits)
    return store


def _hit() -> VectorSearchResult:
    """A search hit carrying the full metadata schema (workspace + document_name + page + classification)."""
    return VectorSearchResult(
        document_id="doc-1",
        chunk_id="doc-1:0",
        content="the retrieved chunk",
        score=0.88,
        metadata={
            "workspace_id": "ws-1",
            "document_name": "Annual Report",
            "page_number": "7",
            "classification": "public",
        },
    )


class TestVectorRetrievalEngine:
    @pytest.mark.asyncio
    async def test_retrieve_embeds_then_workspace_filtered_search(self) -> None:
        """retrieve() embeds the query then searches with a workspace_id payload filter (not clearance).

        Why this test is important:
          - clearance_level is a request filter enforced by FilteringRetrievalEngine, NOT a Qdrant payload
            field; pushing it into the store filter would match zero points and break all dev retrieval.

        What it tests:
          - embed is called with the query; store.search is called with the query embedding + top_k and a
            filter of exactly {"workspace_id": ...}.
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = VectorRetrievalEngine(embedder, store, collection="documents")

        await engine.retrieve("annual revenue", "ws-1", top_k=5, filters={"clearance_level": "public"})

        embedder.embed.assert_awaited_once_with("annual revenue")
        store.search.assert_awaited_once()
        assert store.search.call_args.kwargs["filters"] == {"workspace_id": "ws-1"}
        assert store.search.call_args.kwargs["top_k"] == 5

    @pytest.mark.asyncio
    async def test_retrieve_maps_all_metadata_and_survives_filter(self) -> None:
        """The mapped passage keeps workspace_id/document_name/page_number and survives the security filter.

        Why this test is important:
          - FilteringRetrievalEngine drops any passage whose metadata['workspace_id'] != request; if the
            vector engine dropped that key, every real passage would vanish behind the decorator.

        What it tests:
          - RetrievalResult carries workspace_id in metadata, document_name + int page_number populated, and
            the passage survives FilteringRetrievalEngine at default (public) clearance.
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = FilteringRetrievalEngine(VectorRetrievalEngine(embedder, store, "documents"), min_score=0.5)

        results = await engine.retrieve("q", "ws-1", top_k=5, filters={"clearance_level": "public"})

        assert len(results) == 1
        r = results[0]
        assert r.document_id == "doc-1"
        assert r.document_name == "Annual Report"
        assert r.page_number == 7
        assert r.chunk_content == "the retrieved chunk"
        assert r.metadata["workspace_id"] == "ws-1"

    def test_dimension_mismatch_fails_loudly(self) -> None:
        """A drifted embedder/store dimension raises at construction (single source of truth)."""
        with pytest.raises(ValueError, match="dimension"):
            VectorRetrievalEngine(_embedder(dimension=768), _store([], dimension=3), "documents")


class TestQdrantFactoryBranch:
    def test_from_config_qdrant_builds_filtered_vector_engine(self) -> None:
        """new_retrieval_engine_from_config(kind=qdrant) builds a FilteringRetrievalEngine over the vector engine."""
        engine = new_retrieval_engine_from_config(
            RetrievalConfig(
                kind=RetrievalKind.QDRANT,
                vector_url="http://qdrant:6333",
                vector_collection="documents",
                embedding_host="http://ollama:11434",
                embedding_model="nomic-embed-text",
                embedding_dimension=768,
            ),
        )
        assert isinstance(engine, FilteringRetrievalEngine)
