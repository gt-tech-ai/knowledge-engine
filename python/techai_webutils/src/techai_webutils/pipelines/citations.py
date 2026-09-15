"""Citation extraction from retrieved passages.

Builds a structured ``Citation`` for **every** retrieved passage (not only those the answer's ``[n]``
markers reference), one-to-one and in retrieval order. The generator numbers passages 1-based in the
prompt, so the answer's inline ``[n]`` marker resolves to this list's n-th entry — which is why all
retrieved passages are surfaced as potential citations, and why entries are never collapsed.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.retrieval import Citation, CitationExtractor

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.retrieval import RetrievalResult


class PassageCitationExtractor(CitationExtractor):
    """Maps each retrieved passage to a Citation, one-to-one and in retrieval order."""

    async def extract(self, answer: str, sources: list[RetrievalResult]) -> list[Citation]:  # noqa: ARG002
        """Return one Citation per source passage, in retrieval order.

        The mapping is strictly positional: the generator numbers passages 1-based in the prompt, so
        the n-th entry here is the passage the answer's ``[n]`` marker cites. Collapsing entries (e.g.
        by document or page) would slide every later index onto the wrong passage and drop the
        citations the answer actually references, so distinct passages stay distinct citations.
        """
        return [
            Citation(
                document_id=source.document_id,
                document_name=source.document_name,
                chunk=source.chunk_content,
                page_number=source.page_number,
                confidence=source.score,
                # Trust + provenance ride in the passage metadata (Bedrock sidecar / vector payload);
                # copy them through for the frontend trust badge, clearance display, and source link.
                trust_level=source.metadata.get("trust_level", ""),
                classification=source.metadata.get("classification", ""),
                s3_key=source.metadata.get("s3_key", ""),
                format=source.metadata.get("format", ""),
            )
            for source in sources
        ]
