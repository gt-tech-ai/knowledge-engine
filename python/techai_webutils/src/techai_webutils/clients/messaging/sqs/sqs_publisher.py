"""SQS message publisher using aiobotocore."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Self
import uuid

import aiobotocore.session  # type: ignore[import-untyped]
from techai_webutils.core.interfaces.messaging import MessagePublisher

if TYPE_CHECKING:
    from types import TracebackType

    from techai_webutils.clients.messaging.config import SQSConfig


class SQSPublisher(MessagePublisher):
    """Publishes messages to an SQS queue via aiobotocore.

    Usage::

        async with SQSPublisher(config) as pub:
            await pub.publish("events", b'{"type":"doc.uploaded"}')
    """

    def __init__(self, config: SQSConfig) -> None:
        """Store the SQS connection config; the client is created on context entry."""
        self._config = config
        self._client: object | None = None
        self._session: object | None = None

    async def __aenter__(self) -> Self:
        """Open the underlying aiobotocore SQS client and return self."""
        self._session = aiobotocore.session.get_session()
        self._client = await self._session.create_client(  # type: ignore[union-attr]
            "sqs",
            **self._config.client_kwargs(),
        ).__aenter__()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Close the underlying aiobotocore SQS client on context exit."""
        if self._client is not None:
            await self._client.__aexit__(exc_type, exc_val, exc_tb)  # type: ignore[union-attr]

    async def publish(self, topic: str, payload: bytes) -> None:
        """Send a message to the configured SQS queue."""
        if self._client is None:
            msg = "SQSPublisher not initialized. Use as async context manager."
            raise RuntimeError(msg)
        await self._client.send_message(  # type: ignore[union-attr]
            QueueUrl=self._config.queue_url,
            MessageBody=payload.decode("utf-8"),
            MessageAttributes={
                "topic": {"DataType": "String", "StringValue": topic},
            },
        )

    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Send multiple messages to the configured SQS queue."""
        if self._client is None:
            msg = "SQSPublisher not initialized. Use as async context manager."
            raise RuntimeError(msg)
        entries = [
            {
                "Id": str(uuid.uuid4()),
                "MessageBody": p.decode("utf-8"),
                "MessageAttributes": {
                    "topic": {"DataType": "String", "StringValue": topic},
                },
            }
            for p in payloads
        ]
        # SQS caps a batch at 10 messages; send the chunks concurrently rather than paying a full
        # round-trip per chunk in sequence — an outbox-relay burst of N>10 messages now
        # costs one round-trip, not ceil(N/10).
        await asyncio.gather(
            *(
                self._client.send_message_batch(  # type: ignore[union-attr]
                    QueueUrl=self._config.queue_url,
                    Entries=entries[i : i + 10],
                )
                for i in range(0, len(entries), 10)
            )
        )
