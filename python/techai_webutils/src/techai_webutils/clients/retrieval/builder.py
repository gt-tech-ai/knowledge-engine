"""Env-aware retrieval engine factory (foundation/logger ``NewFromConfig`` pattern).

No Bedrock Knowledge Base exists locally, so dev selects the stub and stage/prod the real Bedrock
engine from config (``RetrievalConfig.kind``). The selected engine is always wrapped in
``FilteringRetrievalEngine`` so server-side re-validation applies in every environment. Unknown kinds
fail loudly. The real Bedrock engine is imported lazily so the dev/stub path never loads the AWS SDK.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.retrieval.filtering import _DEFAULT_MIN_SCORE, FilteringRetrievalEngine
from techai_webutils.clients.retrieval.stub import StubRetrievalEngine

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.clients.retrieval.bedrock.engine import DocumentIdResolver, FilterBuilder
    from techai_webutils.clients.retrieval.filtering import PassagePolicy
    from techai_webutils.core.interfaces.retrieval import RetrievalEngine

# Bedrock similarity scores run materially lower than the local vector (Qdrant) path: a strong
# paraphrase match is ~0.47 and a near-verbatim match ~0.55, so the vector path's 0.5 floor drops
# genuinely relevant passages in stage/prod. Bedrock therefore gets a lower default floor. An explicit
# ``RetrievalConfig.min_score`` still overrides this per environment.
_BEDROCK_MIN_SCORE = 0.4
"""Default relevance-score floor for the Bedrock engine (lower than the vector path's 0.5)."""


class RetrievalKind(StrEnum):
    """Which retrieval engine implementation to build."""

    STUB = "stub"
    """The deterministic no-Bedrock engine (dev/local; fixed workspace-scoped passages)."""
    BEDROCK = "bedrock"
    """The real AWS Bedrock Knowledge Base retrieval engine (stage/prod)."""
    QDRANT = "qdrant"
    """The local Qdrant vector store fed by an Ollama embedder (dev vector-dev stack)."""


@dataclass(frozen=True, slots=True)
class RetrievalConfig:
    """Retrieval engine configuration resolved from ``retrieval.kb.*``."""

    kind: RetrievalKind = RetrievalKind.STUB
    """Selects the engine implementation (``stub`` in dev, ``bedrock`` in stage/prod)."""
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
    min_score: float | None = None
    """Relevance floor for the filtering decorator; passages below it are dropped. ``None`` selects the
    engine-kind default (Bedrock gets a lower floor than the vector/stub path — see ``_BEDROCK_MIN_SCORE``)."""
    # Local vector-dev stack (``kind == QDRANT``): a Qdrant store fed by an Ollama embedder; ignored
    # by the stub/bedrock kinds. Hosts default to localhost; the dev overlay points them at the
    # ``qdrant`` / ``ollama`` Compose services.
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
    policies: Sequence[PassagePolicy] | None = None,
    filter_builder: FilterBuilder | None = None,
    document_id_resolver: DocumentIdResolver | None = None,
) -> RetrievalEngine:
    """Build the retrieval engine selected by ``config.kind``, wrapped in the filtering decorator.

    The consumer's seams (code, not config data) ride alongside: ``policies`` replace the filtering
    decorator's default rules; ``filter_builder`` and ``document_id_resolver`` reach the Bedrock
    engine (ignored by the other kinds). ``None`` keeps each default.
    """
    inner: RetrievalEngine
    if config.kind is RetrievalKind.STUB:
        inner = StubRetrievalEngine()
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
        inner = _build_vector_engine(config)
    else:
        msg = f"unknown retrieval kind: {config.kind!r}"
        raise ValueError(msg)
    return FilteringRetrievalEngine(inner, min_score=_resolve_min_score(config), policies=policies)


def _resolve_min_score(config: RetrievalConfig) -> float:
    """Resolve the relevance floor: an explicit ``config.min_score`` wins; else the engine-kind default."""
    if config.min_score is not None:
        return config.min_score
    return _BEDROCK_MIN_SCORE if config.kind is RetrievalKind.BEDROCK else _DEFAULT_MIN_SCORE


def _build_vector_engine(config: RetrievalConfig) -> RetrievalEngine:
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
    return VectorRetrievalEngine(embedder, store, config.vector_collection)
