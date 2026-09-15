"""Document-status callback port — the API ``InternalService`` status/metadata write surface.

Ingestion (and, later, retrieval / the bulk Ray job) call the API's ``InternalService`` to advance a
document's processing status and record post-parse metadata. The concrete gRPC implementation is
proto-bound (mirroring ``pkg/go/clients/identity``) and lives with its consumer — the ingestion
worker — injected at the composition root; this core module defines only the proto-free Protocol.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol, runtime_checkable


@dataclass(frozen=True, slots=True)
class PendingDocument:
    """A document still awaiting KB indexing (status ``queued_for_indexing``).

    Carries the document id and the S3 object key the reconciler matches against the source URI in
    the Bedrock document listing — the pair the API's ``ListDocumentsPendingIndexing`` RPC returns.
    """

    document_id: str
    """Identifier of the document awaiting indexing."""
    s3_key: str
    """The document's S3 object key, matched against the source URI in the Bedrock listing."""


@dataclass(frozen=True, slots=True)
class ChunkSpec:
    """One chunk to create under a parent document (the ``CreateDocumentChunks`` request spec)."""

    chunk_index: int
    """The 0-based position of this chunk within the parent document."""

    filename: str
    """The suggested object filename; the API pins a ``.md`` extension."""

    page_start: int | None
    """Inclusive 1-based start of the source page range (``None`` for non-paginated content)."""
    page_end: int | None
    """Inclusive 1-based end of the source page range (``None`` for non-paginated content)."""

    size_bytes: int
    """The chunk content's byte size."""


@dataclass(frozen=True, slots=True)
class CreatedChunk:
    """One created (or already-existing, on a redrive) chunk row returned by ``CreateDocumentChunks``.

    Carries the chunk row's id + its S3 object key so the caller can write the chunk's content/sidecar
    to that key and mark it queued_for_indexing.
    """

    document_id: str
    """Identifier of the newly created (or already-existing) chunk row."""
    s3_key: str
    """The chunk row's S3 object key, where the caller writes the chunk's content/sidecar."""
    chunk_index: int
    """The 0-based position of this chunk within its parent document."""


@runtime_checkable
class DocumentStatusUpdater(Protocol):
    """Calls back the API ``InternalService`` to update document status + metadata."""

    async def update_status(self, document_id: str, status: str, failure_reason: str = "") -> None:
        """Set the document's processing status (with a reason when failed)."""
        ...

    async def update_metadata(
        self,
        document_id: str,
        *,
        page_count: int,
        word_count: int,
        language: str,
    ) -> None:
        """Record a document's extracted metadata (page/word count; language is sidecar-only)."""
        ...


@runtime_checkable
class PendingDocumentLister(Protocol):
    """Reads the API's ``queued_for_indexing`` cohort — the reconciler's read side of InternalService."""

    async def list_pending_documents(self, *, page_size: int = 0) -> list[PendingDocument]:
        """Return every document still awaiting indexing, paging through the API cohort to the end."""
        ...


@runtime_checkable
class DocumentChunkCreator(Protocol):
    """Creates chunk rows + relocates the parent for a large document via ``InternalService``."""

    async def create_document_chunks(
        self, parent_document_id: str, chunks: list[ChunkSpec]
    ) -> list[CreatedChunk]:
        """Create N chunk rows under a parent, returning each created chunk's id + key (idempotent)."""
        ...

    async def update_document_storage_key(self, document_id: str, storage_key: str) -> None:
        """Repoint a document at a new object key — the relocated raw parent (idempotent)."""
        ...


@runtime_checkable
class DocumentStatusReader(Protocol):
    """Reads a document's current status + connector source etag by id (the read-side port).

    The read complement of ``DocumentStatusUpdater``: the ingestion DB idempotency store matches the
    returned ``(status, source_etag)`` to skip re-download of an unchanged, already-indexed object.
    """

    async def get_document_status(self, document_id: str) -> tuple[str, str]:
        """Return ``(status, source_etag)`` for a document by id; a missing document surfaces as NotFound."""
        ...


@runtime_checkable
class DocumentStatusClient(
    DocumentStatusUpdater,
    PendingDocumentLister,
    DocumentChunkCreator,
    DocumentStatusReader,
    Protocol,
):
    """The full InternalService write+read surface — the union of the four narrower ports.

    A single backend (the gRPC ``InternalService`` client) implements all four, so this composed
    Protocol is what ``new_document_status_from_config`` returns: the factory contract is an interface
    (parity with the Go ``NewFromConfig`` factories, which return ``interfaces.DatabasePool`` /
    ``RPCClient``), and a second backend selected by ``DocumentStatusKind`` returns the same type.
    Consumers still narrow to whichever single port they depend on (``DocumentStatusUpdater``,
    ``PendingDocumentLister``, ``DocumentChunkCreator``, ``DocumentStatusReader``); this union is a
    subtype of each.
    """
