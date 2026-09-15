"""All-stubs zero-network build proof (D7 / charter §13.2).

Proves the stub-first property: with every swappable client tier selected as its stub/memory/noop
backend (the ``configs/test.yaml`` overlay), the tier factories a composition root calls build
successfully AND construct NO real network backend. Each real backend's constructor is patched to
fail, so if a factory wrongly took the real path the test would raise.
"""

from __future__ import annotations

import pytest
from techai_webutils.clients.email.builder import EmailConfig, EmailKind, new_email_from_config
from techai_webutils.clients.email.noop import NoopEmailSender
from techai_webutils.clients.embedding.builder import (
    EmbeddingConfig,
    EmbeddingKind,
    new_embedding_from_config,
)
from techai_webutils.clients.embedding.stub import StubEmbeddingProvider
from techai_webutils.clients.messaging.builder import (
    MessagingConfig,
    MessagingKind,
    new_messaging_from_config,
    new_messaging_subscriber_from_config,
)
from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.messaging.memory import InMemoryPublisher, InMemorySubscriber
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

# Every real backend constructor, patched to fail — none may be called on the all-stubs path.
_REAL_BACKENDS = [
    "techai_webutils.clients.storage.s3.s3_client.S3StorageClient",
    "techai_webutils.clients.messaging.sqs.sqs_publisher.SQSPublisher",
    "techai_webutils.clients.messaging.sqs.sqs_subscriber.SQSSubscriber",
    "techai_webutils.clients.embedding.ollama.OllamaEmbeddingProvider",
    "techai_webutils.clients.vector.qdrant.QdrantVectorStore",
    "techai_webutils.clients.email.ses.SesEmailSender",
    "techai_webutils.clients.email.smtp.SmtpEmailSender",
]


def test_service_builds_all_stubs_no_network(monkeypatch: pytest.MonkeyPatch) -> None:
    """Every swappable tier builds via its factory under all-stubs, constructing no real backend.

    Why this test is important:
        - Stub-first (charter §13.2) is the property that makes the whole tree buildable/testable with
          no infra. If any tier factory silently fell back to its real backend under the all-stubs
          selection, a "no-infra" build would dial a network the environment does not have.

    What it tests:
        - With every real backend constructor patched to fail, the storage/messaging/embedding/vector/
          email factories called with the stub/memory/noop kind each return the stub implementation —
          i.e. none constructed a real client.
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

    assert isinstance(storage, InMemoryStorageClient)
    assert isinstance(publisher, InMemoryPublisher)
    assert isinstance(subscriber, InMemorySubscriber)
    assert isinstance(embedding, StubEmbeddingProvider)
    assert isinstance(vector, StubVectorStore)
    assert isinstance(email, NoopEmailSender)
