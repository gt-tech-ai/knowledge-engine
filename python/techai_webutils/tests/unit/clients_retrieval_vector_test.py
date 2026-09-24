"""Tests for the vector-backed RetrievalEngine + the qdrant factory branch (mocked deps)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.retrieval.filtering import FilteringRetrievalEngine, MetadataEquals, MinScore
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
    """A search hit carrying a scope key, a document name and a page."""
    return VectorSearchResult(
        document_id="doc-1",
        chunk_id="doc-1:0",
        content="the retrieved chunk",
        score=0.88,
        metadata={
            "tenant": "t1",
            "document_name": "Annual Report",
            "page_number": "7",
        },
    )


class TestVectorRetrievalEngine:
    @pytest.mark.asyncio
    async def test_retrieve_pushes_down_only_the_named_filters(self) -> None:
        """retrieve() embeds the query, then searches with only the filters named in filter_keys.

        Why this test is important:
          - A request filter that is not a stored payload field (e.g. a caller level) matches no points
            when pushed down, so pushing every filter would return nothing; only payload keys go down.

        What it tests:
          - embed is called with the query; store.search gets the top_k and exactly {"tenant": ...} for a
            request carrying tenant + max_level with filter_keys=("tenant",).
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = VectorRetrievalEngine(embedder, store, collection="documents", filter_keys=("tenant",))

        await engine.retrieve("annual revenue", top_k=5, filters={"tenant": "t1", "max_level": "high"})

        embedder.embed.assert_awaited_once_with("annual revenue")
        store.search.assert_awaited_once()
        assert store.search.call_args.kwargs["filters"] == {"tenant": "t1"}
        assert store.search.call_args.kwargs["top_k"] == 5

    @pytest.mark.asyncio
    async def test_retrieve_maps_all_metadata_and_survives_filter(self) -> None:
        """The mapped passage keeps its metadata, name and page, and survives a scope policy.

        Why this test is important:
          - A MetadataEquals scope policy drops any passage whose scope key does not match the request; if
            the vector engine dropped that key, every real passage would vanish behind the decorator.

        What it tests:
          - The RetrievalResult carries the tenant key, document_name and an int page_number, and survives
            MinScore(0.5) + MetadataEquals("tenant").
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = FilteringRetrievalEngine(
            VectorRetrievalEngine(embedder, store, "documents"), [MinScore(0.5), MetadataEquals("tenant")]
        )

        results = await engine.retrieve("q", top_k=5, filters={"tenant": "t1"})

        assert len(results) == 1
        r = results[0]
        assert r.document_id == "doc-1"
        assert r.document_name == "Annual Report"
        assert r.page_number == 7
        assert r.chunk_content == "the retrieved chunk"
        assert r.metadata["tenant"] == "t1"

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
            policies=[],
        )
        assert isinstance(engine, FilteringRetrievalEngine)
