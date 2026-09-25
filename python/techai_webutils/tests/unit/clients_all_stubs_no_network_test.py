"""All-stubs zero-network build proof (ARCHITECTURE.md#stub-first-backends).

Proves the stub-first property: with every swappable client tier selected as its stub/memory/noop
backend (as a test-environment config overlay would), the tier factories a composition root calls build
successfully AND construct NO real network backend. Each real backend's constructor is patched to
fail, so if a factory wrongly took the real path the test would raise.
"""

from __future__ import annotations

import pytest
from techai_webutils.clients.cache.builder import CacheConfig, CacheKind, new_cache_from_config
from techai_webutils.clients.cache.local import LocalCache
from techai_webutils.clients.cache.null import NullCache
from techai_webutils.clients.email.builder import EmailConfig, EmailKind, new_email_from_config
from techai_webutils.clients.email.noop import NoopEmailSender
from techai_webutils.clients.embedding.builder import (
    EmbeddingConfig,
    EmbeddingKind,
    new_embedding_from_config,
)
from techai_webutils.clients.embedding.stub import StubEmbeddingProvider
from techai_webutils.clients.jobs.builder import new_jobs_from_config
from techai_webutils.clients.jobs.config import JobConfig, JobKind
from techai_webutils.clients.jobs.memory import InMemoryJobEnqueuer
from techai_webutils.clients.kb_ingestion.builder import KbConfig, KbKind, new_kb_ingestor_from_config
from techai_webutils.clients.kb_ingestion.noop import StubKnowledgeBaseIngestor
from techai_webutils.clients.llm.builder import LlmConfig, LlmKind, new_llm_from_config
from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.clients.lock import InMemoryLock, LockConfig, LockKind, new_lock_from_config
from techai_webutils.clients.messaging.builder import (
    MessagingConfig,
    MessagingKind,
    new_messaging_from_config,
    new_messaging_subscriber_from_config,
)
from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.messaging.memory import InMemoryPublisher, InMemorySubscriber
from techai_webutils.clients.retrieval import (
    FilteringRetrievalEngine,
    RetrievalConfig,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.storage.builder import (
    StorageConfig,
    StorageKind,
    new_storage_from_config,
)
from techai_webutils.clients.storage.config import S3Config
from techai_webutils.clients.storage.memory import InMemoryStorageClient
from techai_webutils.clients.vector.builder import (
    VectorStoreConfig,
    VectorStoreKind,
    new_vector_store_from_config,
)
from techai_webutils.clients.vector.stub import StubVectorStore
from techai_webutils.execution.executor.asyncio_executor import AsyncioExecutor
from techai_webutils.execution.executor.factory import ExecutorConfig, ExecutorKind, executor_from_config
from techai_webutils.foundation.metrics.builder import MetricsConfig, MetricsKind, new_metrics_from_config
from techai_webutils.foundation.metrics.null_metrics import NullMetricsProvider
from techai_webutils.foundation.tracer.builder import TracerConfig, TracerKind, new_tracer_from_config
from techai_webutils.foundation.tracer.null_tracer import NullTracerProvider

# Every real backend constructor (or the factory hook that builds it), patched to fail — none may be
# called on the all-stubs path.
_REAL_BACKENDS = [
    "techai_webutils.clients.storage.s3.s3_client.S3StorageClient",
    "techai_webutils.clients.messaging.sqs.sqs_publisher.SQSPublisher",
    "techai_webutils.clients.messaging.sqs.sqs_subscriber.SQSSubscriber",
    "techai_webutils.clients.embedding.ollama.OllamaEmbeddingProvider",
    "techai_webutils.clients.vector.qdrant.QdrantVectorStore",
    "techai_webutils.clients.email.ses.SesEmailSender",
    "techai_webutils.clients.email.smtp.SmtpEmailSender",
    "techai_webutils.clients.llm.bedrock.BedrockLlmProvider",
    "techai_webutils.clients.llm.ollama.OllamaLlmProvider",
    "techai_webutils.clients.retrieval.bedrock.BedrockRetrievalEngine",
    "techai_webutils.clients.retrieval.vector.VectorRetrievalEngine",
    "techai_webutils.clients.kb_ingestion.bedrock.BedrockKnowledgeBaseIngestor",
    "techai_webutils.clients.lock.postgres.PostgresAdvisoryLock",
    "techai_webutils.clients.cache.builder.RedisCache",
    "techai_webutils.foundation.tracer.builder.new_tracer",
    "techai_webutils.foundation.metrics.builder.PrometheusMetricsProvider",
    "techai_webutils.execution.executor.factory.ray_runtime_from_config",
]


def test_service_builds_all_stubs_no_network(monkeypatch: pytest.MonkeyPatch) -> None:
    """Every swappable tier builds via its factory under all-stubs, constructing no real backend.

    Why this test is important:
        - Stub-first (ARCHITECTURE.md#stub-first-backends) is the property that makes the whole tree buildable/testable with
          no infra. If any tier factory silently fell back to its real backend under the all-stubs
          selection, a "no-infra" build would dial a network the environment does not have.

    What it tests:
        - With every real backend constructor patched to fail, each tier factory called with its
          stub/memory/noop/null/local/asyncio kind (storage, messaging, embedding, vector, email, llm,
          retrieval, kb_ingestion, lock, cache, jobs, tracer, metrics, executor) returns the stub
          implementation — i.e. none constructed a real client.
    """

    def _boom(*_args: object, **_kwargs: object) -> object:
        msg = "a real network backend was constructed under the all-stubs selection"
        raise AssertionError(msg)

    for target in _REAL_BACKENDS:
        monkeypatch.setattr(target, _boom)

    storage = new_storage_from_config(
        StorageConfig(s3=S3Config(endpoint="", bucket="b", region="r"), kind=StorageKind.MEMORY),
    )
    publisher = new_messaging_from_config(
        MessagingConfig(sqs=SQSConfig(endpoint="", region="r", queue_url=""), kind=MessagingKind.MEMORY),
    )
    subscriber = new_messaging_subscriber_from_config(
        MessagingConfig(sqs=SQSConfig(endpoint="", region="r", queue_url=""), kind=MessagingKind.MEMORY),
    )
    embedding = new_embedding_from_config(EmbeddingConfig(kind=EmbeddingKind.STUB, dimension=8))
    vector = new_vector_store_from_config(VectorStoreConfig(kind=VectorStoreKind.STUB, dimension=8))
    email = new_email_from_config(EmailConfig(kind=EmailKind.NOOP))
    llm = new_llm_from_config(LlmConfig(kind=LlmKind.STUB))
    retrieval = new_retrieval_engine_from_config(RetrievalConfig(kind=RetrievalKind.STUB), policies=[])
    kb_ingestor = new_kb_ingestor_from_config(KbConfig(kind=KbKind.STUB))
    lock = new_lock_from_config(LockConfig(kind=LockKind.MEMORY))
    local_cache = new_cache_from_config(CacheConfig(kind=CacheKind.LOCAL))
    null_cache = new_cache_from_config(CacheConfig(kind=CacheKind.NULL))
    jobs = new_jobs_from_config(JobConfig(kind=JobKind.MEMORY))
    tracer = new_tracer_from_config(TracerConfig(kind=TracerKind.NULL))
    metrics = new_metrics_from_config(MetricsConfig(kind=MetricsKind.NULL))
    executor = executor_from_config(ExecutorConfig(kind=ExecutorKind.ASYNCIO))

    assert isinstance(storage, InMemoryStorageClient)
    assert isinstance(publisher, InMemoryPublisher)
    assert isinstance(subscriber, InMemorySubscriber)
    assert isinstance(embedding, StubEmbeddingProvider)
    assert isinstance(vector, StubVectorStore)
    assert isinstance(email, NoopEmailSender)
    assert isinstance(llm, StubLlmProvider)
    assert isinstance(retrieval, FilteringRetrievalEngine)
    assert isinstance(kb_ingestor, StubKnowledgeBaseIngestor)
    assert isinstance(lock, InMemoryLock)
    assert isinstance(local_cache, LocalCache)
    assert isinstance(null_cache, NullCache)
    assert isinstance(jobs, InMemoryJobEnqueuer)
    assert isinstance(tracer, NullTracerProvider)
    assert isinstance(metrics, NullMetricsProvider)
    assert isinstance(executor, AsyncioExecutor)
