"""Bedrock Knowledge Base retrieval engine (bedrock-agent-runtime Retrieve).

Thin aiobotocore wrapper used in stage/prod (dev uses the stub). Builds the workspace_id
metadata filter and maps Bedrock retrieval results to ``RetrievalResult``; classification is
re-validated server-side by ``FilteringRetrievalEngine`` (defense-in-depth). Carved out of
unit coverage; exercised against real Bedrock in staging.
"""

from __future__ import annotations

from typing import Any, Protocol, Self, cast

import aiobotocore.session  # type: ignore[import-untyped]

from techai_webutils.clients.retrieval.filtering import (
    CLEARANCE_FILTER_KEY,
    allowed_classifications_for,
)
from techai_webutils.core.errors import InternalError
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult


class _BedrockAgentRuntimeClient(Protocol):
    """The bedrock-agent-runtime client slice this engine calls (aiobotocore is untyped for it).

    ``create_client("bedrock-agent-runtime")`` has no typed overload (it resolves to ``Never``), so
    the yielded client is cast to this Protocol to describe the single ``retrieve`` call we make.
    """

    async def retrieve(
        self,
        *,
        knowledgeBaseId: str,
        retrievalQuery: dict[str, str],
        retrievalConfiguration: dict[str, object],
    ) -> dict[str, object]:
        """Retrieve passages; returns a payload carrying ``retrievalResults``."""
        ...


class BedrockRetrievalEngine(RetrievalEngine):
    """RetrievalEngine backed by AWS Bedrock Knowledge Base ``Retrieve``.

    A single bedrock-agent-runtime client is opened lazily on first use and reused across queries
    (opening one per request would repeat credential resolution + connection setup on the hot query
    path); call ``aclose`` on shutdown to release it.
    """

    def __init__(
        self,
        *,
        region: str,
        knowledge_base_id: str,
        endpoint: str | None = None,
        search_type: str = "semantic",
        reranking_model: str = "",
    ) -> None:
        """Store the region, KB id, endpoint, and search strategy; the client opens lazily on first use.

        ``search_type`` selects semantic (vector-only) vs "hybrid" (vector+keyword, Retrieve's
        overrideSearchType); ``reranking_model`` is a Cohere reranker model id (e.g.
        ``cohere.rerank-v3-5:0``) whose presence enables Retrieve's rerankingConfiguration — both are
        config-selected (audit R6/R14). Empty ``reranking_model`` = no reranking.
        """
        self._region = region
        self._kb_id = knowledge_base_id
        self._endpoint = endpoint
        self._search_type = search_type
        self._reranking_model = reranking_model
        self._session = aiobotocore.session.get_session()
        # The client's async context manager + the entered client (typed ``Any`` — aiobotocore is unstubbed).
        self._client_cm: Any = None
        self._client: _BedrockAgentRuntimeClient | None = None

    async def _runtime_client(self) -> _BedrockAgentRuntimeClient:
        """Return the shared bedrock-agent-runtime client, opening (and caching) it on first use."""
        if self._client is None:
            self._client_cm = self._session.create_client(
                "bedrock-agent-runtime",
                region_name=self._region,
                endpoint_url=self._endpoint,
            )
            self._client = cast("_BedrockAgentRuntimeClient", await self._client_cm.__aenter__())
        return self._client

    async def aclose(self) -> None:
        """Close the shared client if one was opened (idempotent)."""
        if self._client_cm is not None:
            await self._client_cm.__aexit__(None, None, None)
            self._client_cm = None
            self._client = None

    async def __aenter__(self) -> Self:
        """Enter the async context; the shared client opens lazily on first call."""
        return self

    async def __aexit__(self, *exc: object) -> None:
        """Release the shared client on context exit."""
        await self.aclose()

    async def retrieve(
        self,
        query: str,
        workspace_id: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        knowledge_base_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve workspace + clearance-scoped passages from the Bedrock Knowledge Base.

        ``knowledge_base_id`` overrides the construction-time KB per call (per-org routing);
        ``None`` falls back to the configured ``self._kb_id``. With neither, this raises an
        ``InternalError`` rather than silently querying an unintended KB — under the per-org (``output``)
        topology the engine is constructed with no default, so a missed upstream fail-closed check errors
        here instead of leaking another tenant's KB (defense-in-depth).

        Builds the Retrieve request from the config-selected strategy: numberOfResults (the wide pool),
        an andAll metadata filter (workspace isolation + the R11 clearance push-down), overrideSearchType
        for hybrid search, and a Cohere rerankingConfiguration when a reranker is configured.
        """
        kb_id = knowledge_base_id or self._kb_id
        if not kb_id:
            msg = "no knowledge base id: per-call id is empty and the engine has no configured default"
            raise InternalError(msg)
        client = await self._runtime_client()
        vector_config: dict[str, object] = {
            "numberOfResults": top_k,
            "filter": self._metadata_filter(workspace_id, filters),
        }
        if self._search_type == "hybrid":
            vector_config["overrideSearchType"] = "HYBRID"
        if self._reranking_model:
            vector_config["rerankingConfiguration"] = {
                "type": "BEDROCK_RERANKING_MODEL",
                "bedrockRerankingConfiguration": {
                    "modelConfiguration": {
                        "modelArn": (
                            f"arn:aws:bedrock:{self._region}::foundation-model/{self._reranking_model}"
                        ),
                    },
                },
            }
        response = await client.retrieve(
            knowledgeBaseId=kb_id,
            retrievalQuery={"text": query},
            retrievalConfiguration={"vectorSearchConfiguration": vector_config},
        )
        results = cast("list[dict[str, object]]", response.get("retrievalResults", []))
        return [_to_result(item, workspace_id) for item in results]

    def _metadata_filter(self, workspace_id: str, filters: dict[str, str] | None) -> dict[str, object]:
        """Build the KB metadata filter: workspace isolation + (R11) the clearance push-down.

        Combines the workspace_id equality with an ``in`` clause over the classifications at/below the
        caller's clearance (``filters[CLEARANCE_FILTER_KEY]``) via ``andAll``, so the KB returns only
        eligible passages before they consume the result budget — the ``FilteringRetrievalEngine``
        decorator re-validates as defense-in-depth. With no clearance in ``filters`` only the workspace
        clause applies (the decorator still enforces the default). The allowed-classification set comes
        from the SAME table the decorator uses, so the pre-filter and the decorator cannot drift.
        """
        workspace_clause: dict[str, object] = {"equals": {"key": "workspace_id", "value": workspace_id}}
        clearance = (filters or {}).get(CLEARANCE_FILTER_KEY)
        if not clearance:
            return workspace_clause
        return {
            "andAll": [
                workspace_clause,
                {"in": {"key": "classification", "value": allowed_classifications_for(clearance)}},
            ]
        }


def _to_float(value: object) -> float:
    """Coerce a JSON number to float, defaulting to 0.0."""
    return float(value) if isinstance(value, (int, float)) else 0.0


_MIN_KEY_SEGMENTS_AFTER_WORKSPACE = 2
"""Minimum path segments (``<document_id>/<filename>``) a valid key has after the workspace prefix."""


def _document_id_from_s3_key(s3_key: str, workspace_id: str) -> str:
    """Derive the document id from the S3 key layout ``{workspace_id}/{document_id}/{filename}``.

    The Bedrock KB chunk metadata carries ``s3_key`` (and ``workspace_id``) but never a bare
    ``document_id``, so a passage/citation would otherwise return with an empty ``document_id`` and lose
    all provenance (a citation could not be traced back to its document). The id is the path segment
    immediately after the workspace id. Returns "" when the key does not match the expected layout.
    """
    remainder = s3_key.removeprefix(f"{workspace_id}/") if workspace_id else s3_key
    segments = remainder.split("/")
    # Expect at least ``<document_id>/<filename>``; a bare key (no doc segment) yields no id.
    if len(segments) >= _MIN_KEY_SEGMENTS_AFTER_WORKSPACE and segments[0]:
        return segments[0]
    return ""


def _to_result(item: dict[str, object], workspace_id: str) -> RetrievalResult:
    """Map one Bedrock retrievalResult to a RetrievalResult."""
    metadata_raw = item.get("metadata", {})
    metadata = {str(k): str(v) for k, v in metadata_raw.items()} if isinstance(metadata_raw, dict) else {}
    metadata.setdefault("workspace_id", workspace_id)
    content = item.get("content", {})
    text = content.get("text", "") if isinstance(content, dict) else ""
    page = metadata.get("page_number")
    # Bedrock metadata has no ``document_id`` key; derive it from ``s3_key`` so passages/citations carry
    # provenance. Prefer an explicit ``document_id`` if a future sidecar adds one.
    document_id = metadata.get("document_id") or _document_id_from_s3_key(
        metadata.get("s3_key", ""), workspace_id
    )
    return RetrievalResult(
        document_id=document_id,
        document_name=metadata.get("document_name", ""),
        chunk_content=str(text),
        score=_to_float(item.get("score", 0.0)),
        page_number=int(page) if page is not None and page.isdigit() else None,
        metadata=metadata,
    )
