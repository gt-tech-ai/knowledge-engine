"""Tests for the retrieval engine: stub, env-aware factory, and security filtering."""

from typing import Any
from unittest.mock import AsyncMock, create_autospec

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult

from techai_webutils.clients.retrieval.bedrock.engine import _to_result
from techai_webutils.clients.retrieval.builder import (
    _BEDROCK_MIN_SCORE,
    RetrievalConfig,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.retrieval.filtering import _DEFAULT_MIN_SCORE, FilteringRetrievalEngine
from techai_webutils.clients.retrieval.stub import StubRetrievalEngine


def _result(doc: str, score: float, classification: str, workspace: str = "ws-1") -> RetrievalResult:
    """Build a RetrievalResult with the given score/classification/workspace."""
    return RetrievalResult(
        document_id=doc,
        document_name=doc,
        chunk_content="chunk",
        score=score,
        page_number=None,
        metadata={"workspace_id": workspace, "classification": classification, "trust_level": "high"},
    )


class TestStubRetrievalEngine:
    @pytest.mark.asyncio
    async def test_returns_workspace_scoped_passages_with_trust(self) -> None:
        """Test that the stub returns passages scoped to the requested workspace, with trust level.

        **Why this test is important:**
          - Dev has no Bedrock KB; the stub is what makes retrieval runnable locally, and its
            passages must be workspace-tagged (for the filter) and carry trust_level (for the badge).

        **What it tests:**
          - Every returned passage has the requested workspace_id and a trust_level.
        """
        results = await StubRetrievalEngine().retrieve("q", "ws-42")
        assert results
        assert all(r.metadata["workspace_id"] == "ws-42" for r in results)
        assert all("trust_level" in r.metadata for r in results)


class TestBedrockKBRouting:
    """Per-call knowledge_base_id selection + the no-fallback fail-closed backstop."""

    def _engine(self, kb_default: str) -> Any:
        """A BedrockRetrievalEngine with a pre-set mock client (bypasses the lazy aiobotocore open)."""
        from techai_webutils.clients.retrieval.bedrock.engine import BedrockRetrievalEngine

        engine = BedrockRetrievalEngine(region="us-east-1", knowledge_base_id=kb_default)
        engine._client = AsyncMock()  # noqa: SLF001 - inject the mocked bedrock-agent-runtime client
        engine._client.retrieve = AsyncMock(return_value={"retrievalResults": []})  # noqa: SLF001
        return engine

    @pytest.mark.asyncio
    async def test_uses_per_call_kb_over_construction_default(self) -> None:
        """Test that a per-call knowledge_base_id overrides the construction-time KB.

        **Why this test is important:**
          - Per-org routing selects the tenant's KB per request; if the engine ignored the per-call id
            and always queried its construction KB, every tenant would hit the same (wrong) KB.

        **What it tests:**
          - ``retrieve(..., knowledge_base_id="kb-per-call")`` issues Retrieve against ``kb-per-call``.
        """
        engine = self._engine("kb-default")

        await engine.retrieve("q", "ws-1", knowledge_base_id="kb-per-call")

        assert engine._client.retrieve.await_args.kwargs["knowledgeBaseId"] == "kb-per-call"  # noqa: SLF001

    @pytest.mark.asyncio
    async def test_uses_construction_kb_when_no_per_call_id(self) -> None:
        """Test that the construction-time KB is used when no per-call id is given (shared topology).

        **Why this test is important:**
          - The shared topology passes no per-call id; the engine must keep querying its configured KB
            so shared-KB behavior is unchanged.

        **What it tests:**
          - ``retrieve(...)`` with no ``knowledge_base_id`` issues Retrieve against the construction KB.
        """
        engine = self._engine("kb-default")

        await engine.retrieve("q", "ws-1")

        assert engine._client.retrieve.await_args.kwargs["knowledgeBaseId"] == "kb-default"  # noqa: SLF001

    @pytest.mark.asyncio
    async def test_raises_when_no_kb_configured_or_passed(self) -> None:
        """Test that an engine with no default KB, called with no per-call id, raises (never falls back).

        **Why this test is important:**
          - Under the per-org (``output``) topology the engine is built with no default KB; a missed
            upstream fail-closed check must error here, not silently query some other KB — the
            engine-level backstop for the cross-tenant-leak invariant.

        **What it tests:**
          - ``retrieve(..., knowledge_base_id=None)`` on a no-default engine raises ``InternalError`` and
            never issues the Retrieve call.
        """
        from techai_webutils.core.errors import InternalError

        engine = self._engine("")

        with pytest.raises(InternalError):
            await engine.retrieve("q", "ws-1", knowledge_base_id=None)
        engine._client.retrieve.assert_not_awaited()  # noqa: SLF001


class TestFilteringRetrievalEngine:
    @pytest.mark.asyncio
    async def test_forwards_knowledge_base_id_to_inner(self) -> None:
        """Test that the filter decorator forwards a per-call knowledge_base_id to the inner engine.

        **Why this test is important:**
          - Per-org routing passes the resolved KB through the filtering decorator to the engine; if the
            decorator dropped it, every routed query would hit the wrong (construction-time) KB.

        **What it tests:**
          - ``FilteringRetrievalEngine.retrieve(..., knowledge_base_id="kb-x")`` calls the inner engine's
            ``retrieve`` with ``knowledge_base_id="kb-x"``.
        """
        inner = create_autospec(RetrievalEngine, instance=True)
        inner.retrieve = AsyncMock(return_value=[])
        engine = FilteringRetrievalEngine(inner)

        await engine.retrieve("q", "ws-1", knowledge_base_id="kb-x")

        inner.retrieve.assert_awaited_once_with("q", "ws-1", 10, None, "kb-x")

    @pytest.mark.asyncio
    async def test_excludes_below_score_threshold(self) -> None:
        """Test that passages below the relevance floor are excluded.

        **Why this test is important:**
          - Low-relevance passages pollute answers; the score floor is the quality gate.

        **What it tests:**
          - With min_score=0.5 every returned passage scores >= 0.5 (the 0.40 stub passage is dropped).
        """
        engine = FilteringRetrievalEngine(StubRetrievalEngine(), min_score=0.5)
        results = await engine.retrieve("q", "ws-1", filters={"clearance_level": "restricted"})
        assert results
        assert all(r.score >= 0.5 for r in results)

    @pytest.mark.asyncio
    async def test_classification_gated_by_clearance(self) -> None:
        """Test that a passage above the caller's clearance is filtered out (and allowed when cleared).

        **Why this test is important:**
          - Clearance-gated retrieval is a security control: an 'internal'-cleared user must never
            see 'confidential' passages, even if the vector store returns them.

        **What it tests:**
          - internal clearance excludes the confidential passage; confidential clearance includes it.
        """
        engine = FilteringRetrievalEngine(StubRetrievalEngine())
        internal = await engine.retrieve("q", "ws-1", filters={"clearance_level": "internal"})
        assert all(r.metadata["classification"] != "confidential" for r in internal)
        confidential = await engine.retrieve("q", "ws-1", filters={"clearance_level": "confidential"})
        assert any(r.metadata["classification"] == "confidential" for r in confidential)

    @pytest.mark.asyncio
    async def test_unrecognized_clearance_fails_closed_to_public(self) -> None:
        """Test that an unrecognized/blank caller clearance is treated as public (least privilege), not most.

        **Why this test is important:**
          - The clearance ceiling must fail CLOSED. If a bad clearance value reaches the filter — e.g. the
            servicer forwarding the proto enum int instead of its lowercase name (the reproduced clearance
            bypass) — the caller must see only public passages, never everything. Reusing the
            document-classification default (most restricted) for the ceiling would fail OPEN and clear
            every classification.

        **What it tests:**
          - With an inner engine returning a public + an internal passage, an unrecognized clearance, a
            blank clearance, a missing key, and no filters each keep only the public passage.
        """
        inner = create_autospec(RetrievalEngine, instance=True)
        inner.retrieve = AsyncMock(
            return_value=[_result("pub", 0.9, "public"), _result("int", 0.9, "internal")]
        )
        engine = FilteringRetrievalEngine(inner, min_score=0.5)

        for bad in ({"clearance_level": "bogus"}, {"clearance_level": ""}, {}, None):
            kept = await engine.retrieve("q", "ws-1", filters=bad)
            assert [r.document_id for r in kept] == ["pub"], f"filters={bad!r} must fail closed to public"

    @pytest.mark.asyncio
    async def test_revalidates_workspace_no_cross_leak(self) -> None:
        """Test that a cross-workspace passage from the inner engine is dropped (security-critical).

        **Why this test is important:**
          - Even if the vector store's own filter misbehaves and returns another workspace's
            passage, server-side re-validation must prevent the cross-tenant leak.

        **What it tests:**
          - An inner engine returning a passage tagged with a different workspace yields zero results.
        """
        inner = create_autospec(RetrievalEngine, instance=True)
        inner.retrieve.return_value = [_result("leak", 0.99, "public", workspace="OTHER-WS")]
        results = await FilteringRetrievalEngine(inner).retrieve(
            "q",
            "ws-1",
            filters={"clearance_level": "restricted"},
        )
        assert results == []

    @pytest.mark.asyncio
    async def test_unclassified_passage_denied_even_to_top_clearance(self) -> None:
        """Test that a passage with no classification is denied to everyone (fail closed).

        **Why this test is important:**
          - An unlabeled document is a data gap, not a public one; treating absent classification as
            'restricted' (rank 3) would leak it to top-clearance users. Fail-closed means it's most-
            restricted (denied) until it is classified — the security-correct default.

        **What it tests:**
          - An inner passage with empty classification + top workspace + high score is dropped even for
            a caller with the highest ('restricted') clearance.
        """
        unclassified = RetrievalResult(
            document_id="unlabeled",
            document_name="unlabeled",
            chunk_content="chunk",
            score=0.99,
            page_number=None,
            metadata={"workspace_id": "ws-1", "trust_level": "high"},  # no classification key
        )
        inner = create_autospec(RetrievalEngine, instance=True)
        inner.retrieve.return_value = [unclassified]
        results = await FilteringRetrievalEngine(inner).retrieve(
            "q",
            "ws-1",
            filters={"clearance_level": "restricted"},
        )
        assert results == []


class TestRetrievalFactory:
    def test_stub_kind_wraps_stub_in_filter(self) -> None:
        """Test that kind=stub builds a filtering engine wrapping the stub (dev).

        **Why this test is important:**
          - Dev must get the stub, always behind the security filter — the env-aware seam.

        **What it tests:**
          - The engine is a FilteringRetrievalEngine whose inner is a StubRetrievalEngine.
        """
        engine = new_retrieval_engine_from_config(RetrievalConfig(kind=RetrievalKind.STUB))
        assert isinstance(engine, FilteringRetrievalEngine)
        assert isinstance(engine._inner, StubRetrievalEngine)  # noqa: SLF001

    def test_bedrock_kind_wraps_bedrock_in_filter(self) -> None:
        """Test that kind=bedrock builds a filtering engine wrapping the Bedrock engine (stage/prod).

        **What it tests:**
          - The engine is a FilteringRetrievalEngine whose inner is a BedrockRetrievalEngine.
        """
        from techai_webutils.clients.retrieval.bedrock import BedrockRetrievalEngine

        engine = new_retrieval_engine_from_config(
            RetrievalConfig(kind=RetrievalKind.BEDROCK, knowledge_base_id="kb"),
        )
        assert isinstance(engine, FilteringRetrievalEngine)
        assert isinstance(engine._inner, BedrockRetrievalEngine)  # noqa: SLF001

    def test_unknown_kind_raises(self) -> None:
        """Test that an unknown retrieval kind fails loudly.

        **What it tests:**
          - A bogus kind raises ValueError naming the kind.
        """
        with pytest.raises(ValueError, match="retrieval kind"):
            new_retrieval_engine_from_config(RetrievalConfig(kind="bogus"))  # type: ignore[arg-type]

    def test_bedrock_kind_uses_lower_default_min_score(self) -> None:
        """kind=bedrock (min_score unset) uses the lower Bedrock floor, not the vector-path default.

        **Why this test is important:**
          - Bedrock similarity scores run lower than the local vector path; the 0.5 vector floor drops
            genuinely relevant passages in stage/prod. The floor must default per engine kind.

        **What it tests:**
          - A bedrock config with ``min_score=None`` yields a filter whose floor is ``_BEDROCK_MIN_SCORE``,
            while a stub config yields ``_DEFAULT_MIN_SCORE``.
        """
        bedrock = new_retrieval_engine_from_config(
            RetrievalConfig(kind=RetrievalKind.BEDROCK, knowledge_base_id="kb"),
        )
        stub = new_retrieval_engine_from_config(RetrievalConfig(kind=RetrievalKind.STUB))
        assert isinstance(bedrock, FilteringRetrievalEngine)
        assert isinstance(stub, FilteringRetrievalEngine)
        assert bedrock._min_score == _BEDROCK_MIN_SCORE  # noqa: SLF001
        assert stub._min_score == _DEFAULT_MIN_SCORE  # noqa: SLF001
        assert _BEDROCK_MIN_SCORE < _DEFAULT_MIN_SCORE

    def test_explicit_min_score_overrides_kind_default(self) -> None:
        """An explicit ``config.min_score`` wins over the engine-kind default.

        **What it tests:**
          - Setting ``min_score=0.7`` on a bedrock config produces a filter floor of 0.7 (not the default).
        """
        engine = new_retrieval_engine_from_config(
            RetrievalConfig(kind=RetrievalKind.BEDROCK, knowledge_base_id="kb", min_score=0.7),
        )
        assert isinstance(engine, FilteringRetrievalEngine)
        assert engine._min_score == 0.7  # noqa: SLF001

    def test_bedrock_config_threads_search_type_and_reranker(self) -> None:
        """kind=bedrock threads the config-selected search_type + reranker into the Bedrock engine.

        **Why this test is important:**
          - HYBRID search + the reranker are the single biggest precision lever, and the One Idea requires
            them to be config-selected (not inlined). If the factory dropped either, staging would silently
            fall back to semantic-only, no-rerank retrieval despite the config.

        **What it tests:**
          - A bedrock config with search_type=hybrid + reranking_kind=bedrock_rerank + a model id builds a
            BedrockRetrievalEngine carrying that search_type and reranker model.
        """
        from techai_webutils.clients.retrieval.bedrock import BedrockRetrievalEngine

        engine = new_retrieval_engine_from_config(
            RetrievalConfig(
                kind=RetrievalKind.BEDROCK,
                knowledge_base_id="kb",
                search_type="hybrid",
                reranking_kind="bedrock_rerank",
                reranking_model="cohere.rerank-v3-5:0",
            ),
        )
        assert isinstance(engine, FilteringRetrievalEngine)
        inner = engine._inner  # noqa: SLF001
        assert isinstance(inner, BedrockRetrievalEngine)
        assert inner._search_type == "hybrid"  # noqa: SLF001
        assert inner._reranking_model == "cohere.rerank-v3-5:0"  # noqa: SLF001

    def test_reranking_kind_none_disables_reranker_even_with_a_model_id(self) -> None:
        """reranking_kind=none disables the reranker regardless of a stray model id (the kind gates it).

        **What it tests:**
          - A bedrock config with reranking_kind=none but a model id set builds an engine with NO reranker
            (empty reranking_model), so a leftover model id can't silently re-enable reranking.
        """
        from techai_webutils.clients.retrieval.bedrock import BedrockRetrievalEngine

        engine = new_retrieval_engine_from_config(
            RetrievalConfig(
                kind=RetrievalKind.BEDROCK,
                knowledge_base_id="kb",
                reranking_kind="none",
                reranking_model="cohere.rerank-v3-5:0",
            ),
        )
        assert isinstance(engine, FilteringRetrievalEngine)
        inner = engine._inner  # noqa: SLF001
        assert isinstance(inner, BedrockRetrievalEngine)
        assert inner._reranking_model == ""  # noqa: SLF001


class TestBedrockResultMapping:
    def test_to_result_derives_document_id_from_s3_key(self) -> None:
        """A Bedrock retrieval result carries a document_id derived from its s3_key.

        **Why this test is important:**
          - Bedrock chunk metadata has an ``s3_key`` but no ``document_id``; without deriving it, every
            passage/citation returns with an empty document_id and loses provenance (a citation cannot be
            traced back to its document —).

        **What it tests:**
          - ``_to_result`` extracts the document id (the segment after the workspace id) from the
            ``{workspace_id}/{document_id}/{filename}`` s3_key layout.
        """
        item: dict[str, object] = {
            "content": {"text": "chunk"},
            "score": 0.55,
            "metadata": {
                "workspace_id": "ws-1",
                "s3_key": "ws-1/doc-42/report.txt",
                "classification": "internal",
                "trust_level": "high",
            },
        }
        result = _to_result(item, "ws-1")
        assert result.document_id == "doc-42"

    def test_to_result_empty_document_id_when_s3_key_absent(self) -> None:
        """With no s3_key and no document_id, the id is empty (no guessing).

        **What it tests:**
          - ``_to_result`` returns an empty document_id when the metadata carries neither key.
        """
        item: dict[str, object] = {
            "content": {"text": "c"},
            "score": 0.5,
            "metadata": {"workspace_id": "ws-1"},
        }
        assert _to_result(item, "ws-1").document_id == ""


class TestAllowedClassificationsFor:
    """The shared clearance→allowed-classifications table (the KB `in` push-down + the decorator agree)."""

    def test_returns_the_labels_at_or_below_the_clearance(self) -> None:
        """allowed_classifications_for expands a clearance to the classification labels at/below it.

        **Why this test is important:**
          - This is the value list the engine's KB `in` metadata filter uses to pre-drop ineligible
            passages. It must mirror the decorator's ranking EXACTLY (a doc is admissible iff its rank <=
            the clearance rank), so the push-down and the decorator cannot disagree — they share this table.

        **What it tests:**
          - internal → {public, internal}; public → {public}; restricted → all four labels.
        """
        from techai_webutils.clients.retrieval.filtering import allowed_classifications_for

        assert set(allowed_classifications_for("public")) == {"public"}
        assert set(allowed_classifications_for("internal")) == {"public", "internal"}
        assert set(allowed_classifications_for("restricted")) == {
            "public",
            "internal",
            "confidential",
            "restricted",
        }


class TestBedrockRetrievalEngineRequest:
    """The Bedrock KB Retrieve request shape (mocked bedrock-agent-runtime client, no AWS)."""

    @staticmethod
    async def _vector_config(
        *,
        search_type: str = "semantic",
        reranking_model: str = "",
        filters: dict[str, str] | None = None,
    ) -> dict[str, Any]:
        """Drive engine.retrieve against a mocked client and return the vectorSearchConfiguration sent."""
        from techai_webutils.clients.retrieval.bedrock.engine import BedrockRetrievalEngine

        engine = BedrockRetrievalEngine(
            region="us-east-1",
            knowledge_base_id="kb-1",
            search_type=search_type,
            reranking_model=reranking_model,
        )
        client = AsyncMock()
        client.retrieve.return_value = {"retrievalResults": []}
        engine._client = client  # noqa: SLF001 - inject the mocked bedrock-agent-runtime client
        await engine.retrieve("q", "ws-1", top_k=25, filters=filters)
        config = client.retrieve.call_args.kwargs["retrievalConfiguration"]
        return config["vectorSearchConfiguration"]

    @pytest.mark.asyncio
    async def test_semantic_request_omits_override_and_rerank(self) -> None:
        """A semantic engine sends numberOfResults + a workspace filter, with no override or rerank.

        **Why this test is important:**
          - overrideSearchType and rerankingConfiguration are opt-in; sending them on the default semantic
            path would change behavior (or error) for every dev/semantic deployment.

        **What it tests:**
          - The default request carries numberOfResults=top_k + the workspace equals filter, and NEITHER
            overrideSearchType NOR rerankingConfiguration.
        """
        vsc = await self._vector_config()
        assert vsc["numberOfResults"] == 25
        assert vsc["filter"] == {"equals": {"key": "workspace_id", "value": "ws-1"}}
        assert "overrideSearchType" not in vsc
        assert "rerankingConfiguration" not in vsc

    @pytest.mark.asyncio
    async def test_hybrid_request_sets_override_search_type(self) -> None:
        """search_type=hybrid sets Retrieve's overrideSearchType=HYBRID (vector + keyword).

        **What it tests:**
          - A hybrid engine's request carries overrideSearchType == "HYBRID".
        """
        vsc = await self._vector_config(search_type="hybrid")
        assert vsc["overrideSearchType"] == "HYBRID"

    @pytest.mark.asyncio
    async def test_rerank_request_sets_the_cohere_reranking_configuration(self) -> None:
        """A configured reranker sets the Cohere bedrockRerankingConfiguration with the model ARN.

        **Why this test is important:**
          - There is no in-repo precedent for this request shape; the exact nesting
            (rerankingConfiguration.type + bedrockRerankingConfiguration.modelConfiguration.modelArn) was
            verified against AWS's own KB-retrieval MCP server. A wrong shape AccessDenies/validation-errors.

        **What it tests:**
          - reranking_model builds rerankingConfiguration.type == BEDROCK_RERANKING_MODEL and the reranker
            model ARN under bedrockRerankingConfiguration.modelConfiguration.modelArn.
        """
        vsc = await self._vector_config(reranking_model="cohere.rerank-v3-5:0")
        rerank = vsc["rerankingConfiguration"]
        assert rerank["type"] == "BEDROCK_RERANKING_MODEL"
        model_arn = rerank["bedrockRerankingConfiguration"]["modelConfiguration"]["modelArn"]
        assert model_arn == "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0"

    @pytest.mark.asyncio
    async def test_clearance_pushes_an_andAll_in_filter_into_the_kb(self) -> None:
        """A caller clearance pushes an andAll(workspace, in-allowed-classifications) filter to the KB.

        **Why this test is important:**
          - Pushing clearance into the KB filter means ineligible passages never consume the (small) result
            budget, and the `in` value list is drawn from the SAME table the decorator uses, so the two agree.

        **What it tests:**
          - With clearance=internal the filter is andAll(workspace equals, classification in {public,internal}).
        """
        vsc = await self._vector_config(filters={"clearance_level": "internal"})
        clauses = vsc["filter"]["andAll"]
        assert {"equals": {"key": "workspace_id", "value": "ws-1"}} in clauses
        in_clause = next(c for c in clauses if "in" in c)
        assert in_clause["in"]["key"] == "classification"
        assert set(in_clause["in"]["value"]) == {"public", "internal"}


class TestRetrievalSeams:
    """Consumer-supplied passage policies, Bedrock filter and document-id resolution."""

    @staticmethod
    def _tenant_passage(doc: str, tenant: str, score: float = 0.9) -> RetrievalResult:
        """A passage labelled only with a consumer-defined ``tenant`` key (no classification)."""
        return RetrievalResult(
            document_id=doc,
            document_name=doc,
            chunk_content="chunk",
            score=score,
            page_number=None,
            metadata={"tenant": tenant},
        )

    def test_to_result_reads_document_id_after_the_workspace_segment(self) -> None:
        """The default document id is the key segment after the workspace, wherever the workspace sits.

        **Why this test is important:**
          - A consumer that prefixes its keys (``{org}/{workspace}/{doc}/{file}``) got the prefix back as
            the document id, so every citation pointed at the wrong document (seen on a real deployment).

        **What it tests:**
          - ``org_x/ws-1/doc-42/report.txt`` resolves to ``doc-42``.
        """
        item: dict[str, object] = {
            "content": {"text": "chunk"},
            "score": 0.55,
            "metadata": {"workspace_id": "ws-1", "s3_key": "org_x/ws-1/doc-42/report.txt"},
        }
        assert _to_result(item, "ws-1").document_id == "doc-42"

    @pytest.mark.asyncio
    async def test_custom_policies_replace_the_default_rules(self) -> None:
        """Consumer policies decide which passages survive, instead of the built-in rules.

        **Why this test is important:**
          - Which metadata isolates tenants and what counts as sensitive are consumer decisions; with
            fixed rules a consumer whose passages carry other labels loses every passage.

        **What it tests:**
          - With MinScore + MetadataEquals("tenant"), unclassified passages of the requested tenant are
            kept, another tenant's passage and a low-score passage are dropped.
        """
        from techai_webutils.clients.retrieval.filtering import MetadataEquals, MinScore

        inner = create_autospec(RetrievalEngine, instance=True)
        inner.retrieve.return_value = [
            self._tenant_passage("a", "t1"),
            self._tenant_passage("b", "t2"),
            self._tenant_passage("c", "t1", score=0.05),
        ]
        engine = FilteringRetrievalEngine(inner, policies=[MinScore(0.1), MetadataEquals("tenant")])

        results = await engine.retrieve("q", "ws-1", filters={"tenant": "t1"})

        assert [r.document_id for r in results] == ["a"]

    def test_ordinal_ceiling_fails_closed_both_ways(self) -> None:
        """OrdinalCeiling admits a passage at or below the caller's level and fails closed on unknowns.

        **Why this test is important:**
          - An unknown passage label must be denied to everyone and an unknown caller level must see
            only the lowest tier; either failing open leaks restricted content.

        **What it tests:**
          - "mid" is admitted for callers at "mid"/"high" and denied at "low".
          - An unknown passage label is denied even at "high"; an unknown caller level acts as "low".
        """
        from techai_webutils.clients.retrieval.filtering import OrdinalCeiling

        policy = OrdinalCeiling("level", "max_level", ("low", "mid", "high"))

        def admits(label: str, caller: str) -> bool:
            passage = RetrievalResult("d", "d", "c", 0.9, None, {"level": label})
            return policy.admits(passage, {"max_level": caller})

        assert admits("mid", "mid")
        assert admits("mid", "high")
        assert not admits("mid", "low")
        assert not admits("unknown", "high")
        assert admits("low", "bogus")
        assert not admits("mid", "bogus")

    @pytest.mark.asyncio
    async def test_bedrock_uses_injected_filter_and_document_id_resolver(self) -> None:
        """The Bedrock engine sends the consumer's KB filter and resolves ids the consumer's way.

        **Why this test is important:**
          - KB metadata keys and object-key layouts belong to the consumer; hard-wiring them in the
            engine is what produced wrong citation ids.

        **What it tests:**
          - The Retrieve request carries the injected filter; results take the injected document id.
        """
        from techai_webutils.clients.retrieval.bedrock.engine import BedrockRetrievalEngine

        engine = BedrockRetrievalEngine(
            region="us-east-1",
            knowledge_base_id="kb-1",
            filter_builder=lambda _ws, f: {"equals": {"key": "tenant", "value": f["tenant"]}},
            document_id_resolver=lambda metadata, _ws: metadata["doc"],
        )
        client = AsyncMock()
        client.retrieve.return_value = {
            "retrievalResults": [{"content": {"text": "c"}, "score": 0.9, "metadata": {"doc": "d9"}}]
        }
        engine._client = client  # noqa: SLF001 - inject the mocked bedrock-agent-runtime client

        results = await engine.retrieve("q", "ws-1", filters={"tenant": "t1"})

        sent = client.retrieve.call_args.kwargs["retrievalConfiguration"]["vectorSearchConfiguration"]
        assert sent["filter"] == {"equals": {"key": "tenant", "value": "t1"}}
        assert [r.document_id for r in results] == ["d9"]

    @pytest.mark.asyncio
    async def test_factory_threads_consumer_seams(self) -> None:
        """The factory hands the consumer's policies, KB filter and id resolver to the engine it builds.

        **Why this test is important:**
          - Consumers build engines through the factory; a seam the factory drops is unusable.

        **What it tests:**
          - A factory-built Bedrock engine sends the injected filter, resolves ids with the injected
            resolver, and filters with the injected policies (an unlabelled passage survives MinScore(0)).
        """
        from techai_webutils.clients.retrieval.filtering import MinScore

        engine = new_retrieval_engine_from_config(
            RetrievalConfig(kind=RetrievalKind.BEDROCK, knowledge_base_id="kb-1"),
            policies=[MinScore(0.0)],
            filter_builder=lambda _ws, _f: {"equals": {"key": "k", "value": "v"}},
            document_id_resolver=lambda metadata, _ws: metadata["doc"],
        )
        client = AsyncMock()
        client.retrieve.return_value = {
            "retrievalResults": [{"content": {"text": "c"}, "score": 0.1, "metadata": {"doc": "d1"}}]
        }
        engine._inner._client = client  # type: ignore[attr-defined]  # noqa: SLF001

        results = await engine.retrieve("q", "ws-1")

        sent = client.retrieve.call_args.kwargs["retrievalConfiguration"]["vectorSearchConfiguration"]
        assert sent["filter"] == {"equals": {"key": "k", "value": "v"}}
        assert [r.document_id for r in results] == ["d1"]

    @pytest.mark.asyncio
    async def test_bedrock_passage_without_workspace_metadata_is_dropped(self) -> None:
        """A Bedrock passage that carries no workspace_id is dropped by the workspace post-filter.

        **Why this test is important:**
          - The post-filter is the defense-in-depth check behind the KB's own filter; if the engine
            filled a missing workspace_id in from the request, every unlabelled passage would pass it
            and the check would never fail closed.

        **What it tests:**
          - Through the factory's default policies, a passage with no workspace_id metadata is dropped,
            while an otherwise identical passage labelled with the request's workspace is kept.
        """
        engine = new_retrieval_engine_from_config(
            RetrievalConfig(kind=RetrievalKind.BEDROCK, knowledge_base_id="kb-1", min_score=0.0)
        )
        client = AsyncMock()
        client.retrieve.return_value = {
            "retrievalResults": [
                {"content": {"text": "a"}, "score": 0.9, "metadata": {"classification": "public"}},
                {
                    "content": {"text": "b"},
                    "score": 0.9,
                    "metadata": {"classification": "public", "workspace_id": "ws-1", "document_id": "d2"},
                },
            ]
        }
        engine._inner._client = client  # type: ignore[attr-defined]  # noqa: SLF001

        results = await engine.retrieve("q", "ws-1")

        assert [r.document_id for r in results] == ["d2"]


class TestBedrockRetrieveContract:
    """The Bedrock KB `Retrieve` response → RetrievalResult contract, on a realistic payload."""

    @staticmethod
    def _retrieve_payload(metadata: dict[str, object]) -> dict[str, object]:
        """A `Retrieve` response shaped like Bedrock's: text content, S3 location, sidecar metadata."""
        return {
            "retrievalResults": [
                {
                    "content": {"text": "Quarterly revenue grew 12%.", "type": "TEXT"},
                    "location": {
                        "type": "S3",
                        "s3Location": {"uri": f"s3://docs-bucket/{metadata.get('s3_key', '')}"},
                    },
                    "score": 0.71,
                    "metadata": {
                        "x-amz-bedrock-kb-source-uri": f"s3://docs-bucket/{metadata.get('s3_key', '')}",
                        "x-amz-bedrock-kb-chunk-id": "1%3A0%3AabcDEF",
                        "x-amz-bedrock-kb-data-source-id": "DS123",
                        **metadata,
                    },
                }
            ],
            "ResponseMetadata": {"HTTPStatusCode": 200},
        }

    async def _retrieve(self, metadata: dict[str, object]) -> RetrievalResult:
        """Run the Bedrock engine over one Retrieve payload (mocked client) and return its only result."""
        from techai_webutils.clients.retrieval.bedrock.engine import BedrockRetrievalEngine

        engine = BedrockRetrievalEngine(region="us-east-1", knowledge_base_id="kb-1")
        client = AsyncMock()
        client.retrieve.return_value = self._retrieve_payload(metadata)
        engine._client = client  # noqa: SLF001 - inject the mocked bedrock-agent-runtime client
        [result] = await engine.retrieve("q", "ws-1")
        return result

    @pytest.mark.asyncio
    async def test_explicit_sidecar_keys_win(self) -> None:
        """A sidecar's explicit document_id and document_name are what the citation carries.

        **Why this test is important:**
          - The sidecar is the authoritative source of a chunk's document identity; deriving it from
            the object key is only the fallback for chunks indexed before the sidecar carried it.

        **What it tests:**
          - With document_id/document_name in the metadata, the result uses them verbatim (even though
            the key would derive a different id), plus the text, score, page and full metadata.
        """
        result = await self._retrieve(
            {
                "workspace_id": "ws-1",
                "s3_key": "org_x/ws-1/doc-from-key/report.pdf",
                "document_id": "doc-42",
                "document_name": "Q3 Report",
                "classification": "internal",
                "page_number": "7",
            }
        )
        assert result.document_id == "doc-42"
        assert result.document_name == "Q3 Report"
        assert result.chunk_content == "Quarterly revenue grew 12%."
        assert result.score == pytest.approx(0.71)
        assert result.page_number == 7
        assert result.metadata["classification"] == "internal"

    @pytest.mark.asyncio
    async def test_legacy_sidecar_derives_the_id_from_the_key_never_the_org(self) -> None:
        """Without explicit keys, the id comes from the per-org key and the name stays empty.

        **Why this test is important:**
          - Chunks indexed before the sidecar carried document_id must still cite the right document;
            the org segment of the key must never be mistaken for it.

        **What it tests:**
          - `{org}/{workspace}/{document}/{file}` yields the document segment; document_name is "".
        """
        result = await self._retrieve({"workspace_id": "ws-1", "s3_key": "org_x/ws-1/doc-7/a/b.pdf"})
        assert result.document_id == "doc-7"
        assert result.document_name == ""


_SEGMENT = st.text(alphabet="abcdefghijklmnopqrstuvwxyz0123456789-_.", min_size=1, max_size=12)


@settings(max_examples=200)
@given(
    org=_SEGMENT.map(lambda s: f"org_{s}"),
    workspace=st.uuids().map(str),
    document=st.uuids().map(str),
    filename=st.lists(_SEGMENT, min_size=1, max_size=3).map("/".join),
    prefixed=st.booleans(),
)
def test_document_id_from_key_is_the_segment_after_the_workspace(
    org: str, workspace: str, document: str, filename: str, prefixed: bool
) -> None:
    """For any key with the workspace segment, the id is the segment after it — never the prefix.

    **Why this test is important:**
      - Key layouts vary (legacy `{workspace}/…`, per-org `{org}/{workspace}/…`, nested filenames);
        a resolver that guessed the first segment returned the org for every per-org key.

    **What it tests:**
      - `[{org}/]{workspace}/{document}/{filename…}` resolves to `document` for both layouts and any
        nested filename, and a key without the workspace resolves to "".
    """
    from techai_webutils.clients.retrieval.bedrock.engine import document_id_from_key

    key = f"{org}/{workspace}/{document}/{filename}" if prefixed else f"{workspace}/{document}/{filename}"
    assert document_id_from_key({"s3_key": key}, workspace) == document
    assert document_id_from_key({"s3_key": f"{org}/{document}/{filename}"}, workspace) == ""
