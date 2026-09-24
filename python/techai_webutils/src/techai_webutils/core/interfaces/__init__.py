"""Core interfaces for the Tech AI Knowledge Engine Python services.

Defines ABC-based abstract contracts for all Python service dependencies.
Implementations live in the foundation or service layer; these interfaces enable
dependency inversion and testability.

Service Interfaces:
    BaseService: Service lifecycle (startup, shutdown, health check).
    CrudService: Generic CRUD operations with authorization.
    ServiceHooks: Lifecycle hooks for CRUD service operations.
    Pipeline: Stateless data transformation (sync and async variants).
    Workflow: Multi-step business process orchestration (sync and async variants).
    DocumentParser: Parse raw documents into text + metadata chunks.
    ChunkingStrategy: Split documents into retrieval-sized chunks.
    EmbeddingProvider: Generate vector embeddings from text.
    KnowledgeBase: Sync documents and query a vector knowledge base.
    LLMProvider: Generate text completions from a language model.
    RetrievalEngine: Orchestrate semantic search with reranking.
    QueryRewriter: Transform user queries for better retrieval.
    IntentClassifier: Classify query intent (search, summarize, compare, etc.).
    CitationExtractor: Extract source citations from LLM responses.
    VectorStore: Low-level vector similarity search operations.

Data Access Interfaces:
    Repository: Generic CRUD data access (business boundary).
    Store: Generic CRUD data access (infrastructure boundary).
    Transaction: Database transaction handle.
    TransactionManager: Transaction lifecycle management.

Foundation Interfaces:
    Cache: Async key-value cache (Redis, in-memory, null).
    CacheInvalidator: Cache invalidation (prefix, all).
    ByteCache: Sync raw-byte cache for infrastructure wrappers.
    Logger: Structured, leveled logging.
    MetricsProvider: Counter, histogram, gauge metric recording.
    TracerProvider: Distributed tracing with spans.
    ConfigLoader: Read-only configuration access.

Resilience Interfaces:
    CircuitBreakerInterface: Fail-fast circuit breaker.
    RateLimiter: Token-bucket rate limiting.
    Bulkhead: Concurrency limiting.
    Retrier: Automatic retry on transient failures.
    Deduplicator: Idempotency for at-least-once delivery.
    LeaderElector: Single-instance execution across replicas.
    Hedger: Tail-latency reduction by racing a backup attempt.
    AdaptiveThrottler: Client-side SRE load-shedding.

Infrastructure Interfaces:
    MessagePublisher: Publish messages to a broker.
    MessageConsumer: Consume messages from a queue.
    EventPublisher: Publish domain events.
    StorageClient: Object storage operations.
    AuthProvider: Authentication and authorization.
    ConnectionManager: WebSocket connection lifecycle.

Job Interfaces:
    Job: Background job definition.
    Worker: Job processor.
    JobEnqueuer: Submit jobs for async processing.
    JobScheduler: Schedule periodic jobs.
    WorkerRegistry: Register workers by kind.

Query Interfaces:
    QueryRouter: Route queries to engines.
    QueryEngine: Execute queries against backends.
    ResponseAssembler: Combine results into responses.
"""

from techai_webutils.core.interfaces.adaptive_throttler import AdaptiveThrottler
from techai_webutils.core.interfaces.auth import AuthIdentity, AuthProvider
from techai_webutils.core.interfaces.bulkhead import Bulkhead
from techai_webutils.core.interfaces.byte_cache import ByteCache
from techai_webutils.core.interfaces.cache import Cache, CacheInvalidator
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.core.interfaces.config_loader import ConfigLoader
from techai_webutils.core.interfaces.connection import ClientConnection, ConnectionManager
from techai_webutils.core.interfaces.crud_service import CrudService, ServiceHooks
from techai_webutils.core.interfaces.dedup import Deduplicator
from techai_webutils.core.interfaces.document import ChunkingStrategy, DocumentParser
from techai_webutils.core.interfaces.embedding import EmbeddingProvider
from techai_webutils.core.interfaces.event_publisher import EventPublisher
from techai_webutils.core.interfaces.hedger import Hedger
from techai_webutils.core.interfaces.jobs import (
    Job,
    JobEnqueuer,
    JobScheduler,
    PeriodicJob,
    Worker,
    WorkerRegistry,
)
from techai_webutils.core.interfaces.knowledge_base import KnowledgeBase
from techai_webutils.core.interfaces.leader import LeaderElector
from techai_webutils.core.interfaces.lifecycle import (
    Client,
    HealthChecker,
    ManagedResource,
)
from techai_webutils.core.interfaces.llm import LLMProvider
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.core.interfaces.messaging import (
    Message,
    MessageConsumer,
    MessageHandler,
    MessagePublisher,
)
from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricGauge,
    MetricHistogram,
    MetricsProvider,
)
from techai_webutils.core.interfaces.pipeline import AsyncPipeline, Pipeline
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
from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.core.interfaces.repository import Repository, Transaction, TransactionManager
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.core.interfaces.retrieval import (
    CitationExtractor,
    IntentClassifier,
    QueryRewriter,
    RetrievalEngine,
)
from techai_webutils.core.interfaces.service import BaseService
from techai_webutils.core.interfaces.storage import StorageClient, StorageObject
from techai_webutils.core.interfaces.store import Store
from techai_webutils.core.interfaces.tracer import TracerProvider, TracerSpan
from techai_webutils.core.interfaces.vector_store import VectorStore
from techai_webutils.core.interfaces.workflow import AsyncWorkflow, Workflow

__all__ = [
    "AdaptiveThrottler",
    # Query
    "AssembledResponse",
    # Service
    "AsyncPipeline",
    "AsyncWorkflow",
    # Infrastructure
    "AuthIdentity",
    "AuthProvider",
    "BaseService",
    # Resilience
    "Bulkhead",
    "ByteCache",
    # Foundation
    "Cache",
    "CacheInvalidator",
    # AI/Retrieval
    "ChunkingStrategy",
    "CircuitBreakerInterface",
    "Citation",
    "CitationExtractor",
    "Client",
    "ClientConnection",
    "ConfigLoader",
    "ConnectionManager",
    "CrudService",
    "Deduplicator",
    "DocumentParser",
    "EmbeddingProvider",
    "EventPublisher",
    "HealthChecker",
    "Hedger",
    "IntentClassifier",
    # Jobs
    "Job",
    "JobEnqueuer",
    "JobScheduler",
    "KnowledgeBase",
    "LLMProvider",
    "LeaderElector",
    "Logger",
    "ManagedResource",
    "Message",
    "MessageConsumer",
    "MessageHandler",
    "MessagePublisher",
    "MetricCounter",
    "MetricGauge",
    "MetricHistogram",
    "MetricsProvider",
    "PeriodicJob",
    "Pipeline",
    "QueryEngine",
    "QueryPlan",
    "QueryRequest",
    "QueryResult",
    "QueryRewriter",
    "QueryRouter",
    "RateLimiter",
    # Data Access
    "Repository",
    "ResponseAssembler",
    "ResultItem",
    "Retrier",
    "RetrievalEngine",
    "ServiceHooks",
    "StorageClient",
    "StorageObject",
    "Store",
    "TracerProvider",
    "TracerSpan",
    "Transaction",
    "TransactionManager",
    "VectorStore",
    "Worker",
    "WorkerRegistry",
    "Workflow",
]
