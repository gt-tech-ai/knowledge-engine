"""Retrieval pipeline interfaces."""

from abc import ABC, abstractmethod
from collections.abc import AsyncIterator, Sequence
from dataclasses import dataclass, field

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass(slots=True)
class RetrievalResult:
    """A single retrieval result with citation information."""

    document_id: str
    """Identifier of the source document this passage was retrieved from."""
    document_name: str
    """Human-readable source document name, for citation display."""
    chunk_content: str
    """The retrieved passage text passed to the answer generator as context."""
    score: float
    """Relevance score of this passage against the query (higher is more relevant)."""
    page_number: int | None
    """Source page the passage appears on (``None`` for non-paginated content)."""
    metadata: dict[str, str]
    """Metadata carried with the chunk (source, classification, and similar)."""


@dataclass(slots=True)
class Citation:
    """A citation referencing a source document (with trust + provenance for frontend display)."""

    document_id: str
    """Identifier of the cited source document."""
    document_name: str
    """Human-readable name of the cited document, for display."""
    chunk: str
    """The specific passage text the citation points to."""
    page_number: int | None
    """Source page the cited passage appears on (``None`` when not paginated/unknown)."""
    confidence: float
    """The extractor's confidence that this citation supports the answer (higher is stronger)."""
    trust_level: str = ""
    """The source document's trust badge value (high/medium/low); ``""`` when unknown."""
    classification: str = ""
    """The source document's sensitivity (public/internal/confidential/restricted)."""
    s3_key: str = ""
    """The source object's storage key, so a citation can link back to the document."""
    format: str = ""
    """The source document's format (pdf/docx/...), for a format icon on the citation."""
    attributes: dict[str, str] = field(default_factory=dict)
    """Consumer-chosen passage metadata carried with the citation (see ``PassageCitationExtractor``)."""


@dataclass
class QueryIntent:
    """Classified intent of a user query."""

    intent_type: str  # "factual", "analytical", "comparative", "procedural"
    """The classified intent category: ``factual``, ``analytical``, ``comparative``, or ``procedural``."""
    confidence: float
    """The classifier's confidence in the assigned ``intent_type`` (higher is more certain)."""
    entities: list[str]
    """Named entities the classifier extracted from the query."""
    keywords: list[str]
    """Salient keywords extracted from the query, used to steer retrieval."""


@dataclass(slots=True)
class HistoryTurn:
    """A prior conversation turn, role-tagged for query rewriting + answer generation.

    ``role`` is ``"user"`` or ``"assistant"``; system turns are never carried as history
    (they are injected separately by the prompt strategies —).
    """

    role: str
    """The turn's speaker: ``"user"`` or ``"assistant"`` (system turns are never carried as history)."""
    content: str
    """The turn's message text."""


class RetrievalEngine(ManagedResource, ABC):
    """Abstract retrieval engine for searching documents.

    Phase 1: Vector similarity search with filtering.
    Phase 2+: Hybrid retrieval (vector + keyword), re-ranking, cross-encoder.
    """

    @abstractmethod
    async def retrieve(
        self,
        query: str,
        workspace_id: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        knowledge_base_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve relevant document chunks for a query.

        ``knowledge_base_id`` selects the KB to query per call (per-org routing);
        ``None`` uses the engine's construction-time KB (the shared topology). An engine constructed
        with no default KB and called with ``None`` must fail loudly, not silently fall back.
        """


class CitationExtractor(ABC):
    """Abstract citation extractor for identifying source references.

    Phase 1: Chunk-level citations with document metadata.
    Phase 2+: Sentence-level citations, page number extraction, quote highlighting.
    """

    @abstractmethod
    async def extract(self, answer: str, sources: list[RetrievalResult]) -> list[Citation]:
        """Extract citations from a generated answer against source documents."""


class QueryRewriter(ABC):
    """Abstract query rewriter for improving retrieval quality.

    Phase 1: Simple expansion with synonyms.
    Phase 2+: LLM-powered rewriting, multi-query generation, HyDE.
    """

    @abstractmethod
    async def rewrite(self, query: str, history: Sequence[HistoryTurn] | None = None) -> list[str]:
        """Rewrite a query into one or more optimized variants, given prior conversation turns."""


class IntentClassifier(ABC):
    """Abstract intent classifier for understanding query purpose.

    Phase 1: Rule-based classification.
    Phase 2+: LLM-powered classification, multi-label support.
    """

    @abstractmethod
    async def classify(self, query: str) -> QueryIntent:
        """Classify the intent of a user query."""


class AnswerGenerator(ABC):
    """Abstract answer generator: retrieved passages + query -> a cited answer (RAG generate step).

    Phase 1: single-pass generation with inline citation markers, streaming + unary.
    Phase 2+: multi-step reasoning, tool use, structured output.
    """

    @abstractmethod
    def stream(
        self,
        query: str,
        passages: list[RetrievalResult],
        history: Sequence[HistoryTurn] | None = None,
    ) -> AsyncIterator[str]:
        """Stream the generated answer token-by-token (an async generator; iterate, don't await).

        ``history`` is supplied to the prompt as non-citable context; passages remain the only
        citable source.
        """

    @abstractmethod
    async def complete(
        self,
        query: str,
        passages: list[RetrievalResult],
        history: Sequence[HistoryTurn] | None = None,
    ) -> str:
        """Generate the full (non-streaming) answer from the query, retrieved passages, and history.

        ``history`` is non-citable context; passages remain the only citable source.
        """


class TitleGenerator(ABC):
    """Abstract conversation-title generator: (question, answer) -> a short title.

    Retrieval-free — a plain LLM completion (no passages), used to auto-title a new conversation
    from its first turn. The prompt is injected by the consumer, like the query rewriter.
    """

    @abstractmethod
    async def generate_title(self, query: str, answer: str) -> str:
        """Return a short conversation title for the (query, answer) exchange ("" if none)."""
