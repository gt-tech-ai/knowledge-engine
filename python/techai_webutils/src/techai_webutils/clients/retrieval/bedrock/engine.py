"""Bedrock Knowledge Base retrieval engine (bedrock-agent-runtime Retrieve).

Thin aiobotocore wrapper used in stage/prod (dev uses the stub). Builds the KB metadata filter
(``workspace_clearance_filter`` unless the consumer injects its own) and maps Bedrock retrieval
results to ``RetrievalResult``, resolving each document id with ``document_id_from_key`` unless the
consumer injects a resolver; passages are re-validated server-side by ``FilteringRetrievalEngine``
(defense-in-depth). Carved out of
unit coverage; exercised against real Bedrock in staging.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from typing import Any, Protocol, Self, cast

import aiobotocore.session  # type: ignore[import-untyped]

from techai_webutils.clients.retrieval.filtering import (
    CLEARANCE_FILTER_KEY,
    allowed_classifications_for,
)
from techai_webutils.core.errors import InternalError
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult

FilterBuilder = Callable[[str, Mapping[str, str]], dict[str, object]]
"""Builds the KB metadata filter from ``(workspace_id, filters)``."""

DocumentIdResolver = Callable[[Mapping[str, str], str], str]
"""Resolves a passage's document id from ``(chunk metadata, workspace_id)``."""


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
        filter_builder: FilterBuilder | None = None,
        document_id_resolver: DocumentIdResolver | None = None,
    ) -> None:
        """Store the region, KB id, endpoint, and search strategy; the client opens lazily on first use.

        ``search_type`` selects semantic (vector-only) vs "hybrid" (vector+keyword, Retrieve's
        overrideSearchType); ``reranking_model`` is a Cohere reranker model id (e.g.
        ``cohere.rerank-v3-5:0``) whose presence enables Retrieve's rerankingConfiguration — both are
        config-selected. Empty ``reranking_model`` = no reranking. ``filter_builder`` builds the KB
        metadata filter (default ``workspace_clearance_filter``) and ``document_id_resolver`` resolves
        each passage's document id (default ``document_id_from_key``), so a consumer's metadata keys
        and object-key layout are its own.
        """
        self._region = region
        self._kb_id = knowledge_base_id
        self._endpoint = endpoint
        self._search_type = search_type
        self._reranking_model = reranking_model
        self._build_filter = filter_builder or workspace_clearance_filter
        self._resolve_document_id = document_id_resolver or document_id_from_key
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
        an andAll metadata filter (workspace isolation + the clearance push-down), overrideSearchType
        for hybrid search, and a Cohere rerankingConfiguration when a reranker is configured.
        """
        kb_id = knowledge_base_id or self._kb_id
        if not kb_id:
            msg = "no knowledge base id: per-call id is empty and the engine has no configured default"
            raise InternalError(msg)
        client = await self._runtime_client()
        vector_config: dict[str, object] = {
            "numberOfResults": top_k,
            "filter": self._build_filter(workspace_id, filters or {}),
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
        return [_to_result(item, workspace_id, self._resolve_document_id) for item in results]


def workspace_clearance_filter(workspace_id: str, filters: Mapping[str, str]) -> dict[str, object]:
    """Build the default KB metadata filter: workspace isolation + the clearance push-down.

    Combines the workspace_id equality with an ``in`` clause over the classifications at/below the
    caller's clearance (``filters[CLEARANCE_FILTER_KEY]``) via ``andAll``, so the KB returns only
    eligible passages before they consume the result budget — the ``FilteringRetrievalEngine``
    decorator re-validates as defense-in-depth. With no clearance in ``filters`` only the workspace
    clause applies (the decorator still enforces the default). The allowed-classification set comes
    from the SAME table the decorator uses, so the pre-filter and the decorator cannot drift.
    """
    workspace_clause: dict[str, object] = {"equals": {"key": "workspace_id", "value": workspace_id}}
    clearance = filters.get(CLEARANCE_FILTER_KEY)
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
"""Minimum path segments (``<document_id>/<filename>``) a valid key has after the workspace segment."""


def document_id_from_key(metadata: Mapping[str, str], workspace_id: str) -> str:
    """Resolve a passage's document id: an explicit ``document_id``, else the one in its ``s3_key``.

    The Bedrock KB chunk metadata carries ``s3_key`` (and ``workspace_id``) but usually no bare
    ``document_id``, so a passage/citation would otherwise return with an empty ``document_id`` and lose
    all provenance. The id is the key segment immediately after the workspace segment, wherever that
    segment sits, so ``{workspace}/{doc}/{file}`` and prefixed layouts such as
    ``{org}/{workspace}/{doc}/{file}`` both resolve. With no workspace the first segment is the id.
    Returns "" when the key does not match (no guessing).
    """
    if explicit := metadata.get("document_id"):
        return explicit
    segments = metadata.get("s3_key", "").split("/")
    if not workspace_id:
        start = 0
    elif workspace_id in segments:
        start = segments.index(workspace_id) + 1
    else:
        return ""
    # Expect at least ``<document_id>/<filename>`` after the workspace; a bare key yields no id.
    if len(segments) - start >= _MIN_KEY_SEGMENTS_AFTER_WORKSPACE and segments[start]:
        return segments[start]
    return ""


def _to_result(
    item: dict[str, object],
    workspace_id: str,
    resolve_document_id: DocumentIdResolver = document_id_from_key,
) -> RetrievalResult:
    """Map one Bedrock retrievalResult to a RetrievalResult."""
    metadata_raw = item.get("metadata", {})
    metadata = {str(k): str(v) for k, v in metadata_raw.items()} if isinstance(metadata_raw, dict) else {}
    metadata.setdefault("workspace_id", workspace_id)
    content = item.get("content", {})
    text = content.get("text", "") if isinstance(content, dict) else ""
    page = metadata.get("page_number")
    document_id = resolve_document_id(metadata, workspace_id)
    return RetrievalResult(
        document_id=document_id,
        document_name=metadata.get("document_name", ""),
        chunk_content=str(text),
        score=_to_float(item.get("score", 0.0)),
        page_number=int(page) if page is not None and page.isdigit() else None,
        metadata=metadata,
    )
