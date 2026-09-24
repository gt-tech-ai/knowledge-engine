"""SQS message consumer using aiobotocore long-polling."""

from __future__ import annotations

import asyncio
import logging
from typing import TYPE_CHECKING, Self

import aiobotocore.session  # type: ignore[import-untyped]
from techai_webutils.core.interfaces.messaging import Message, MessageConsumer, MessageHandler

if TYPE_CHECKING:
    from types import TracebackType

    from techai_webutils.clients.messaging.config import SQSConfig

logger = logging.getLogger(__name__)


class SQSSubscriber(MessageConsumer):
    """Consumes messages from an SQS queue via long-polling.

    Usage::

        async with SQSSubscriber(config) as sub:
            await sub.subscribe("events", handler)
    """

    def __init__(self, config: SQSConfig) -> None:
        """Store the SQS connection config; the client is created on context entry."""
        self._config = config
        self._client: object | None = None
        self._session: object | None = None
        self._running = False

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
        """Stop the consumer loop and close the aiobotocore SQS client on exit."""
        self._running = False
        if self._client is not None:
            await self._client.__aexit__(exc_type, exc_val, exc_tb)  # type: ignore[union-attr]

    async def subscribe(self, topic: str, handler: MessageHandler) -> None:
        """Start consuming messages. Blocks until ``close()`` is called."""
        if self._client is None:
            msg = "SQSSubscriber not initialized. Use as async context manager."
            raise RuntimeError(msg)
        self._running = True
        while self._running:
            resp = await self._client.receive_message(  # type: ignore[union-attr]
                QueueUrl=self._config.queue_url,
                MaxNumberOfMessages=self._config.max_messages,
                WaitTimeSeconds=self._config.wait_time_seconds,
                MessageAttributeNames=["All"],
            )
            messages = resp.get("Messages", [])
            if messages:
                # Fan the poll batch out concurrently instead of awaiting each message serially,
                # so a consumer built on this reference subscriber gets batch throughput. Each message still acks (deletes) only on its own
                # handler success; a failing handler is logged and left for redrive. No explicit
                # semaphore is needed — SQS caps a receive batch at 10 (max_messages), which is already
                # a safe fan-out width, so the batch size IS the bound.
                await asyncio.gather(*(self._handle_one(topic, raw, handler) for raw in messages))

    async def _handle_one(self, topic: str, raw: dict, handler: MessageHandler) -> None:  # type: ignore[type-arg]
        """Deliver one message to the handler and delete it on success (log + leave for redrive on error)."""
        if self._client is None:
            return
        msg_obj = Message(
            id=raw["MessageId"],
            topic=topic,
            payload=raw["Body"].encode("utf-8"),
            metadata=self._extract_metadata(raw),
        )
        try:
            await handler(msg_obj)
            await self._client.delete_message(  # type: ignore[union-attr]
                QueueUrl=self._config.queue_url,
                ReceiptHandle=raw["ReceiptHandle"],
            )
        except Exception:
            logger.exception("Failed to process message %s", msg_obj.id)

    async def close(self) -> None:
        """Stop the consumer loop gracefully."""
        self._running = False

    @staticmethod
    def _extract_metadata(raw: dict) -> dict[str, str]:  # type: ignore[type-arg]
        """Extract string attributes from an SQS message."""
        attrs = raw.get("MessageAttributes", {})
        return {
            k: v.get("StringValue", "")
            for k, v in attrs.items()
            if isinstance(v, dict) and v.get("DataType") == "String"
        }
