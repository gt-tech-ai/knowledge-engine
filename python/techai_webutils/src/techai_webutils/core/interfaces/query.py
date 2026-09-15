"""Query orchestration interfaces.

Mirrors Go's ``interfaces.QueryRouter``, ``QueryEngine``,
``ResponseAssembler``, and associated data types.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field


@dataclass
class QueryRequest:
    """An incoming query from a user."""

    query: str
    """The user's natural-language question text."""
    workspace_id: str
    """The workspace to search within (the tenant isolation scope)."""
    user_id: str
    """The user issuing the query, for authorization and clearance filtering."""
    filters: dict[str, str] = field(default_factory=dict)
    """Optional metadata filters that narrow the candidate documents (e.g. by tag or source)."""
    max_results: int = 10
    """Maximum number of results to return from each engine."""


@dataclass
class QueryPlan:
    """Describes how to execute a query across engines."""

    engines: list[str] = field(default_factory=list)
    """Names of the query engines the router selected to run for this query."""
    parameters: dict[str, str] = field(default_factory=dict)
    """Engine-specific execution parameters the router attached to the plan."""
    request: QueryRequest | None = None
    """The originating request the plan was built from (``None`` until bound)."""


@dataclass
class ResultItem:
    """A single result from a query engine."""

    document_id: str
    """Identifier of the source document this result was drawn from."""
    chunk: str
    """The matched chunk text returned by the engine."""
    score: float
    """The engine's relevance score for this result (higher is more relevant)."""
    metadata: dict[str, str] = field(default_factory=dict)
    """Engine-supplied metadata for the result (source, page, and similar)."""


@dataclass
class QueryResult:
    """Raw results from a single engine."""

    engine: str
    """Name of the engine that produced these results."""
    items: list[ResultItem] = field(default_factory=list)
    """The ranked results the engine returned, best first."""
    latency_ms: int = 0
    """Wall-clock time the engine took to produce the results, in milliseconds."""


@dataclass
class Citation:
    """References a specific document and location."""

    document_id: str
    """Identifier of the cited source document."""
    document_name: str
    """Human-readable name of the cited document, for display."""
    chunk: str
    """The specific passage text the citation points to."""
    page_number: int = 0
    """Source page the cited passage appears on (0 when not paginated/unknown)."""


@dataclass
class AssembledResponse:
    """The final response sent to the user."""

    answer: str
    """The synthesized natural-language answer presented to the user."""
    citations: list[Citation] = field(default_factory=list)
    """The citations backing the answer, provenance for each claim."""
    sources: list[str] = field(default_factory=list)
    """Identifiers of the distinct source documents the answer draws on."""


class QueryRouter(ABC):
    """Determines which QueryEngine(s) to invoke for a given query."""

    @abstractmethod
    async def route(self, request: QueryRequest) -> QueryPlan:
        """Determine the execution plan for a query."""
        ...


class QueryEngine(ABC):
    """Processes queries against a specific backend."""

    @abstractmethod
    async def execute(self, plan: QueryPlan) -> QueryResult:
        """Run the query and return raw results."""
        ...

    @abstractmethod
    def name(self) -> str:
        """Return the engine identifier for logging/routing."""
        ...


class ResponseAssembler(ABC):
    """Combines results from multiple engines into a final response."""

    @abstractmethod
    async def assemble(self, results: list[QueryResult]) -> AssembledResponse:
        """Combine query results into a final response."""
        ...
