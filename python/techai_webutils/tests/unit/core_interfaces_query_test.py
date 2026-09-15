"""Tests for query orchestration interfaces and dataclasses."""

from techai_webutils.core.interfaces.query import (
    AssembledResponse,
    Citation,
    QueryEngine,
    QueryPlan,
    QueryRequest,
    QueryResult,
    QueryRouter,
    ResponseAssembler,
    ResultItem,
)
import pytest


class TestQueryRequest:
    def test_construction(self) -> None:
        """Test that QueryRequest defaults to no filters and a 10-result cap.

        **Why this test is important:**
          - QueryRequest is the entry point of the search pipeline; the default max_results
            bounds how much the retrieval engine fetches, so a wrong default would silently
            over- or under-fetch on every unparameterized query
          - The empty-filters default means "search everything" — a populated default would
            unexpectedly narrow results

        **What it tests:**
          - query is stored as given, and omitting filters/max_results yields {} and 10
        """
        req = QueryRequest(query="what is X?", workspace_id="ws-1", user_id="u-1")
        assert req.query == "what is X?"
        assert req.filters == {}
        assert req.max_results == 10

    def test_with_filters(self) -> None:
        """Test that explicit filters and max_results override the defaults.

        **Why this test is important:**
          - Callers narrow searches via filters (e.g. document type) and tune result count;
            if these were ignored the engine would scan unfiltered or return the wrong volume,
            degrading both relevance and latency

        **What it tests:**
          - A supplied filters mapping and max_results are stored and returned unchanged
        """
        req = QueryRequest(
            query="q",
            workspace_id="ws",
            user_id="u",
            filters={"type": "pdf"},
            max_results=5,
        )
        assert req.filters == {"type": "pdf"}
        assert req.max_results == 5


class TestQueryPlan:
    def test_construction(self) -> None:
        """Test that QueryPlan keeps its engine list and defaults parameters/request.

        **Why this test is important:**
          - The QueryPlan is the router's output that tells the orchestrator which engines to
            fan out to; if the engine list were lost, no backend would be queried and the user
            would get an empty answer
          - The None default for request and empty parameters are the documented "not yet
            bound" state the orchestrator checks before execution

        **What it tests:**
          - engines round-trips, and omitting parameters/request yields {} and None
        """
        plan = QueryPlan(engines=["vector", "sql"])
        assert plan.engines == ["vector", "sql"]
        assert plan.parameters == {}
        assert plan.request is None


class TestResultItem:
    def test_construction(self) -> None:
        """Test that ResultItem retains its document/score and defaults metadata.

        **Why this test is important:**
          - ResultItem is a single retrieval hit; its score drives ranking and its document_id
            drives citation provenance, so either being dropped or swapped would surface the
            wrong source or rank in the assembled answer
          - The empty metadata default is what the assembler reads when an engine attaches none

        **What it tests:**
          - document_id and score round-trip, and omitting metadata yields {}
        """
        item = ResultItem(document_id="d1", chunk="text", score=0.95)
        assert item.document_id == "d1"
        assert item.score == 0.95
        assert item.metadata == {}


class TestQueryResult:
    def test_construction(self) -> None:
        """Test that QueryResult records which engine produced it and its latency.

        **Why this test is important:**
          - QueryResult tags each engine's raw output with its source and latency; the engine
            label lets the assembler attribute and weight results per backend, and latency_ms
            feeds per-engine observability used to spot slow retrieval paths

        **What it tests:**
          - engine and latency_ms are stored and returned exactly as constructed
        """
        result = QueryResult(engine="vector", items=[], latency_ms=42)
        assert result.engine == "vector"
        assert result.latency_ms == 42


class TestCitation:
    def test_construction(self) -> None:
        """Test that a Citation built without a page defaults page_number to 0.

        **Why this test is important:**
          - Citations are the provenance shown to users next to a generated answer; for sources
            with no meaningful page (e.g. a whole web doc), the contract is page_number 0 rather
            than a missing or arbitrary value the UI would mis-render

        **What it tests:**
          - Omitting page_number yields the documented default of 0
        """
        cite = Citation(document_id="d1", document_name="doc.pdf", chunk="text")
        assert cite.page_number == 0


class TestAssembledResponse:
    def test_construction(self) -> None:
        """Test that an answer-only AssembledResponse defaults to empty citations/sources.

        **Why this test is important:**
          - AssembledResponse is the final payload streamed to the user; when the model returns
            an answer with no supporting hits, the contract is empty (not None) citation and
            source lists so the WebSocket/UI can iterate them without null-guarding
          - Independent per-instance defaults prevent one response's lists leaking into another

        **What it tests:**
          - Omitting citations and sources yields two empty lists
        """
        resp = AssembledResponse(answer="The answer is X.")
        assert resp.citations == []
        assert resp.sources == []


class TestQueryABCs:
    def test_cannot_instantiate_router(self) -> None:
        """Test that QueryRouter cannot be instantiated without a route() implementation.

        **Why this test is important:**
          - QueryRouter is the contract that decides which engines a query fans out to; an
            instantiable stub returning None would leave the orchestrator with no plan and
            produce empty answers
          - Forces every router to implement real planning logic

        **What it tests:**
          - Instantiating QueryRouter directly raises TypeError because route() is abstract
        """
        with pytest.raises(TypeError):
            QueryRouter()  # type: ignore[abstract]

    def test_cannot_instantiate_engine(self) -> None:
        """Test that QueryEngine cannot be instantiated without execute()/name().

        **Why this test is important:**
          - QueryEngine is the per-backend execution contract; an instantiable stub would
            return no results and no name, so the assembler could neither retrieve hits nor
            attribute them to a source
          - Forces every backend adapter to implement real query execution and identity

        **What it tests:**
          - Instantiating QueryEngine directly raises TypeError because execute() and name()
            are abstract
        """
        with pytest.raises(TypeError):
            QueryEngine()  # type: ignore[abstract]

    def test_cannot_instantiate_assembler(self) -> None:
        """Test that ResponseAssembler cannot be instantiated without assemble().

        **Why this test is important:**
          - ResponseAssembler is the contract that merges multi-engine results into the final
            cited answer; an instantiable stub would emit nothing, breaking the last stage of
            the query pipeline before the response reaches the user

        **What it tests:**
          - Instantiating ResponseAssembler directly raises TypeError because assemble() is
            abstract
        """
        with pytest.raises(TypeError):
            ResponseAssembler()  # type: ignore[abstract]
