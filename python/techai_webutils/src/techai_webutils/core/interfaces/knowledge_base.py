"""Knowledge base interface for managing document collections."""

from abc import ABC, abstractmethod
from dataclasses import dataclass

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass
class KBDocument:
    """A document in the knowledge base."""

    id: str
    """Unique identifier of the document within the knowledge base."""
    workspace_id: str
    """The workspace that owns this document (the tenant isolation scope)."""
    file_name: str
    """The original uploaded filename, shown to users and in citations."""
    content_type: str
    """MIME type of the source file, used to select the parser."""
    status: str  # "pending", "processing", "ready", "error"
    """Indexing lifecycle status: ``pending``, ``processing``, ``ready``, or ``error``."""
    chunk_count: int = 0
    """Number of chunks this document was split into once indexed (0 before indexing)."""
    error_message: str | None = None
    """Failure detail when ``status`` is ``error`` (``None`` otherwise)."""


@dataclass
class KBSyncResult:
    """Result of a knowledge base sync operation."""

    documents_added: int
    """Count of documents newly indexed during the sync."""
    documents_updated: int
    """Count of already-indexed documents whose content was re-indexed."""
    documents_removed: int
    """Count of documents dropped from the index because their source no longer exists."""
    chunks_created: int
    """Total number of chunks produced across all added/updated documents."""
    errors: list[str]
    """Per-document error messages for documents that failed to sync (empty on full success)."""


class KnowledgeBase(ManagedResource, ABC):
    """Abstract knowledge base for managing document embeddings and retrieval.

    Phase 1: Document indexing and status tracking.
    Phase 2+: Incremental updates, versioning, cross-workspace search.
    """

    @abstractmethod
    async def index_document(
        self,
        workspace_id: str,
        document_id: str,
        chunks: list[str],
        classification: str = "public",
        document_name: str = "",
    ) -> None:
        """Index a document's chunks into the knowledge base.

        ``classification`` is stamped onto each stored chunk so the clearance/security filter can admit
        or deny it per the querying user's clearance; it defaults to ``public`` (the lowest bar) for
        callers that have no per-document classification.

        ``document_name`` is the human-readable source name (the uploaded filename) stamped onto each
        chunk so a retrieval citation can display it instead of the opaque ``document_id``; it defaults
        to empty, and an implementation falls back to the ``document_id`` so a caller with no name keeps
        the prior behaviour.
        """

    @abstractmethod
    async def remove_document(self, workspace_id: str, document_id: str) -> None:
        """Remove a document and its chunks from the knowledge base."""

    @abstractmethod
    async def get_document_status(self, workspace_id: str, document_id: str) -> KBDocument | None:
        """Get the indexing status of a document."""

    @abstractmethod
    async def sync(self, workspace_id: str) -> KBSyncResult:
        """Synchronize the knowledge base with the latest document states."""
