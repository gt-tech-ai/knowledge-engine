"""Config-selected retrieval engine factory (foundation/logger ``NewFromConfig`` pattern).

``RetrievalConfig.kind`` selects the engine (a stub, a local vector store, or a Bedrock Knowledge
Base). The selected engine is always wrapped in ``FilteringRetrievalEngine`` with the config's score
floor plus the consumer's scope policies, so server-side re-validation applies in every environment.
Unknown kinds fail loudly. Heavy backends are imported lazily, so the stub path never loads the AWS
SDK or the vector clients.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.retrieval.filtering import FilteringRetrievalEngine, MinScore
from techai_webutils.clients.retrieval.stub import StubRetrievalEngine

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.clients.retrieval.bedrock.engine import DocumentIdResolver, FilterBuilder
    from techai_webutils.clients.retrieval.filtering import PassagePolicy
    from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult


class RetrievalKind(StrEnum):
    """Which retrieval engine implementation to build."""

    STUB = "stub"
    """The in-memory engine over a consumer-supplied corpus (local development and tests)."""
    BEDROCK = "bedrock"
    """The AWS Bedrock Knowledge Base retrieval engine."""
    QDRANT = "qdrant"
    """A Qdrant vector store fed by an Ollama embedder (local vector development)."""


@dataclass(frozen=True, slots=True)
class RetrievalConfig:
    """Retrieval engine configuration resolved from ``retrieval.kb.*``."""

    kind: RetrievalKind = RetrievalKind.STUB
    """Selects the engine implementation."""
    region: str = "us-east-1"
    """AWS region for the Bedrock KB client (unused by the stub)."""
    endpoint: str | None = None
    """Optional Bedrock endpoint override (e.g. a VPC endpoint); ``None`` uses AWS default resolution."""
    knowledge_base_id: str = ""
    """The Bedrock Knowledge Base id to query (empty for the stub)."""
    search_type: str = "semantic"
    """KB search strategy: "semantic" (vector only) | "hybrid" (vector + keyword; the KB's
    OpenSearch storage supports it). "hybrid" sets Retrieve's overrideSearchType."""
    reranking_kind: str = "none"
    """Reranker selection: "none" | "bedrock_rerank" (a Cohere Rerank model reorders the
    candidate pool via Retrieve's rerankingConfiguration)."""
    reranking_model: str = ""
    """Reranker model id for the bedrock_rerank kind, e.g. "cohere.rerank-v3-5:0" (empty → no reranking)."""
    min_score: float = 0.5
    """Relevance floor for the filtering decorator; passages below it are dropped. Backends score on
    different scales (Bedrock's cosine scores run lower than a local vector store's), so set it per
    engine kind."""
    # Local vector stack (``kind == QDRANT``): a Qdrant store fed by an Ollama embedder; ignored by the
    # stub/bedrock kinds. Hosts default to localhost.
    vector_url: str = "http://localhost:6333"
    """Qdrant HTTP endpoint the dev engine reads/writes points from."""
    vector_collection: str = "documents"
    """Qdrant collection name holding the document embeddings."""
    embedding_host: str = "http://localhost:11434"
    """Ollama host serving the embedding model."""
    embedding_model: str = "nomic-embed-text"
    """Embedding model name requested from Ollama."""
    embedding_dimension: int = 768
    """Vector dimensionality of ``embedding_model`` (must match the Qdrant collection)."""


def new_retrieval_engine_from_config(
    config: RetrievalConfig,
    *,
    policies: Sequence[PassagePolicy],
    filter_builder: FilterBuilder | None = None,
    document_id_resolver: DocumentIdResolver | None = None,
    store_filter_keys: Sequence[str] = (),
    stub_passages: Sequence[RetrievalResult] = (),
) -> RetrievalEngine:
    """Build the retrieval engine selected by ``config.kind``, wrapped in the filtering decorator.

    The decorator applies ``MinScore(config.min_score)`` plus ``policies`` — the consumer's scope
    rules (e.g. ``MetadataEquals`` on its tenant key); pass an empty list only when there is no scope
    to enforce. The other seams (code, not config data) reach one kind each: ``filter_builder`` and
    ``document_id_resolver`` the Bedrock engine, ``store_filter_keys`` the vector engine's push-down,
    ``stub_passages`` the stub's corpus.
    """
    inner: RetrievalEngine
    if config.kind is RetrievalKind.STUB:
        inner = StubRetrievalEngine(stub_passages)
    elif config.kind is RetrievalKind.BEDROCK:
        # Lazy import: keep aiobotocore off the dev/stub path (loaded only in stage/prod).
        from techai_webutils.clients.retrieval.bedrock import BedrockRetrievalEngine  # noqa: PLC0415

        inner = BedrockRetrievalEngine(
            region=config.region,
            knowledge_base_id=config.knowledge_base_id,
            endpoint=config.endpoint,
            search_type=config.search_type,
            # The reranker model is passed only when the kind selects it, so kind=none disables reranking
            # regardless of a stray model id.
            reranking_model=(config.reranking_model if config.reranking_kind == "bedrock_rerank" else ""),
            filter_builder=filter_builder,
            document_id_resolver=document_id_resolver,
        )
    elif config.kind is RetrievalKind.QDRANT:
        inner = _build_vector_engine(config, store_filter_keys)
    else:
        msg = f"unknown retrieval kind: {config.kind!r}"
        raise ValueError(msg)
    return FilteringRetrievalEngine(inner, [MinScore(config.min_score), *policies])


def _build_vector_engine(config: RetrievalConfig, filter_keys: Sequence[str]) -> RetrievalEngine:
    """Build the local vector-dev engine (Ollama embedder + Qdrant store) from ``config``.

    The embedding dimension is the single source of truth: both the embedder and the store are built
    with ``config.embedding_dimension``, and ``VectorRetrievalEngine`` asserts they agree. Heavy backends
    (httpx, qdrant-client) are imported lazily so the stub/bedrock paths never load them.
    """
    from techai_webutils.clients.retrieval.vector import VectorRetrievalEngine  # noqa: PLC0415
    from techai_webutils.clients.vector.composition import build_embedder_and_store  # noqa: PLC0415

    embedder, store = build_embedder_and_store(
        embedding_host=config.embedding_host,
        embedding_model=config.embedding_model,
        embedding_dimension=config.embedding_dimension,
        vector_url=config.vector_url,
        collection=config.vector_collection,
    )
    return VectorRetrievalEngine(embedder, store, config.vector_collection, filter_keys=filter_keys)
