"""Tests for passage-based citation extraction (one citation per passage, positionally aligned)."""

import pytest
from techai_webutils.core.interfaces.retrieval import RetrievalResult

from techai_webutils.pipelines.citations import PassageCitationExtractor


def _source(doc: str, page: int | None, score: float = 0.9, chunk: str = "chunk") -> RetrievalResult:
    """Build a RetrievalResult for the given document/page/score/chunk text."""
    return RetrievalResult(
        document_id=doc,
        document_name=f"{doc} name",
        chunk_content=chunk,
        score=score,
        page_number=page,
        metadata={},
    )


class TestPassageCitationExtractor:
    @pytest.mark.asyncio
    async def test_maps_passage_to_citation(self) -> None:
        """Test that a source passage maps to a Citation carrying its page + relevance.

        **Why this test is important:**
          - Page-level provenance is the trust feature; a citation missing the page (or score)
            can't be verified by the user.

        **What it tests:**
          - A single source yields one Citation with the document id, page, and score.
        """
        citations = await PassageCitationExtractor().extract("answer [1]", [_source("doc-1", 4, 0.92)])
        assert len(citations) == 1
        assert citations[0].document_id == "doc-1"
        assert citations[0].page_number == 4
        assert citations[0].confidence == 0.92

    @pytest.mark.asyncio
    async def test_carries_trust_classification_s3key_format(self) -> None:
        """Test that the extractor copies trust_level/classification/s3_key/format from source metadata.

        **Why this test is important:**
          - A citation's trust badge, clearance display, and back-link to the source object all depend
            on these fields; if the extractor drops them, the retrieval gRPC surfaces empty
            values and the frontend can't render trust/provenance.

        **What it tests:**
          - A source whose metadata carries the four keys yields a Citation with them set; absent keys
            default to the empty string.
        """
        source = RetrievalResult(
            document_id="doc-1",
            document_name="Report",
            chunk_content="chunk",
            score=0.9,
            page_number=2,
            metadata={
                "trust_level": "high",
                "classification": "internal",
                "s3_key": "ws-1/doc-1.pdf",
                "format": "pdf",
            },
        )
        [citation] = await PassageCitationExtractor().extract("a", [source])
        assert citation.trust_level == "high"
        assert citation.classification == "internal"
        assert citation.s3_key == "ws-1/doc-1.pdf"
        assert citation.format == "pdf"
        # An absent-metadata source defaults the new fields cleanly (best-effort provenance).
        [bare] = await PassageCitationExtractor().extract("a", [_source("doc-2", None)])
        assert bare.trust_level == ""
        assert bare.s3_key == ""

    @pytest.mark.asyncio
    async def test_emits_one_citation_per_passage_in_order(self) -> None:
        """Test that every passage yields its own citation, positionally aligned with the prompt.

        **Why this test is important:**
          - The generation prompt numbers passages 1-based, so the answer's ``[n]`` marker resolves
            to the n-th citation. Collapsing entries silently breaks that contract two ways: markers
            past the collapse point resolve to the WRONG source (a provenance failure), and markers
            past the end resolve to nothing, rendering as dead plain text in the UI.

        **What it tests:**
          - Four passages of one document yield four citations in retrieval order, so ``[n]`` maps
            to ``sources[n - 1]`` — including passages sharing a document with no page number, which
            a (document, page) collapse would have discarded.
        """
        sources = [_source("doc-1", None, chunk=f"passage {n}") for n in range(1, 5)]
        citations = await PassageCitationExtractor().extract("cites [1] [2] [3] [4]", sources)
        assert [c.chunk for c in citations] == ["passage 1", "passage 2", "passage 3", "passage 4"]

    @pytest.mark.asyncio
    async def test_keeps_repeated_passages_distinct(self) -> None:
        """Test that identical passages are not collapsed into one citation.

        **Why this test is important:**
          - Collapsing is tempting for display tidiness, but the prompt already numbered both copies
            separately; removing one renumbers every later citation and misattributes the answer's
            markers. Alignment outranks tidiness, so duplicates must survive extraction.

        **What it tests:**
          - Two byte-identical sources plus a third yield three citations, not two.
        """
        sources = [_source("doc-1", 4), _source("doc-1", 4), _source("doc-2", None)]
        citations = await PassageCitationExtractor().extract("a", sources)
        assert len(citations) == 3
        assert [c.document_id for c in citations] == ["doc-1", "doc-1", "doc-2"]
