"""Deterministic no-Bedrock retrieval engine for dev/local.

There is no Bedrock Knowledge Base locally, so dev runs this stub: it returns a fixed set of
passages scoped to the requested workspace, with varied classification/trust so the security
filter and trust-badge rendering can be exercised end-to-end without AWS.
"""

from __future__ import annotations

from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

# Each row holds: document id, name, chunk text, score, page, classification, trust level.
_STUB_PASSAGES: tuple[tuple[str, str, str, float, int | None, str, str], ...] = (
    ("doc-1", "Quarterly Report", "Q3 revenue grew 18% year over year.", 0.92, 4, "internal", "high"),
    (
        "doc-2",
        "HR Policy Manual",
        "Confidential compensation guidelines.",
        0.71,
        12,
        "confidential",
        "medium",
    ),
    ("doc-3", "Public FAQ", "General product information for everyone.", 0.40, None, "public", "low"),
)
"""Canned passages (id, name, text, score, page, classification, trust) the stub engine returns."""


class StubRetrievalEngine(NoOpAsyncResource, RetrievalEngine):
    """RetrievalEngine returning fixed, workspace-scoped passages (dev/local)."""

    async def retrieve(
        self,
        query: str,  # noqa: ARG002
        workspace_id: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,  # noqa: ARG002
        knowledge_base_id: str | None = None,  # noqa: ARG002 - stub returns one canned corpus regardless of KB
    ) -> list[RetrievalResult]:
        """Return the canned passages tagged with the requested workspace_id, capped at top_k."""
        results = [
            RetrievalResult(
                document_id=doc_id,
                document_name=name,
                chunk_content=chunk,
                score=score,
                page_number=page,
                metadata={
                    "workspace_id": workspace_id,
                    "classification": classification,
                    "trust_level": trust,
                },
            )
            for doc_id, name, chunk, score, page, classification, trust in _STUB_PASSAGES
        ]
        return results[:top_k]
