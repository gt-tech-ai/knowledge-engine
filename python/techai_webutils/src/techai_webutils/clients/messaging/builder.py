"""Messaging client builder — ``new_messaging_from_config`` (shape; mirrors Go ``messaging.NewFromConfig``).

The tier-root factory: selects a message-broker backend by ``MessagingKind`` (SQS today) and returns
the ``MessagePublisher`` / ``MessageConsumer`` interfaces. The concrete backend lives in the ``sqs/``
subpackage, imported lazily so selecting a different kind never loads aiobotocore. Unknown kinds fail
loudly, matching the Go ``NewFromConfig`` contract.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.messaging import MessageConsumer, MessagePublisher


class MessagingKind(StrEnum):
    """Which message-broker backend to build."""

    SQS = "sqs"
    """AWS SQS (real SQS in cloud, ElasticMQ in dev)."""
    MEMORY = "memory"
    """In-process broker (dev/test/all-stubs; no external broker) — ."""


@dataclass(frozen=True, slots=True)
class MessagingConfig:
    """Messaging configuration: the selected backend + its settings."""

    sqs: SQSConfig
    """SQS backend settings (endpoint, region, queue URL, credentials)."""
    kind: MessagingKind = MessagingKind.SQS
    """Selects the backend. The zero value is SQS."""


def new_messaging_from_config(config: MessagingConfig) -> MessagePublisher:
    """Build the ``MessagePublisher`` selected by ``config.kind`` (heavy backend imported lazily)."""
    if config.kind is MessagingKind.SQS:
        from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher  # noqa: PLC0415 — lazy: skip aiobotocore until selected

        return SQSPublisher(config.sqs)
    if config.kind is MessagingKind.MEMORY:
        from techai_webutils.clients.messaging.memory import InMemoryBroker, InMemoryPublisher  # noqa: PLC0415 — lazy, and no SDK to load

        return InMemoryPublisher(InMemoryBroker())
    msg = f"unknown messaging kind: {config.kind!r}"
    raise ValueError(msg)


def new_messaging_subscriber_from_config(config: MessagingConfig) -> MessageConsumer:
    """Build the ``MessageConsumer`` selected by ``config.kind`` (heavy backend imported lazily)."""
    if config.kind is MessagingKind.SQS:
        from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber  # noqa: PLC0415 — lazy: skip aiobotocore until selected

        return SQSSubscriber(config.sqs)
    if config.kind is MessagingKind.MEMORY:
        from techai_webutils.clients.messaging.memory import InMemoryBroker, InMemorySubscriber  # noqa: PLC0415 — lazy, and no SDK to load

        return InMemorySubscriber(InMemoryBroker())
    msg = f"unknown messaging kind: {config.kind!r}"
    raise ValueError(msg)
