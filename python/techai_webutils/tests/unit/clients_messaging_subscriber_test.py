"""Tests for SQS subscriber with mocked aiobotocore."""

from __future__ import annotations

from typing import TYPE_CHECKING

import pytest

from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber

if TYPE_CHECKING:
    from unittest.mock import AsyncMock

    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.messaging import Message


async def _metadata_of(
    config: SQSConfig, aws_client: AsyncMock, raw: dict[str, object]
) -> dict[str, str]:
    """Deliver ``raw`` through ``subscribe`` once; return the metadata the handler received."""
    aws_client.receive_message.return_value = {"Messages": [raw]}
    received: list[Message] = []
    async with SQSSubscriber(config) as subscriber:

        async def handler(msg: Message) -> None:
            received.append(msg)
            await subscriber.close()

        await subscriber.subscribe("events", handler)
    [message] = received
    return message.metadata


class TestSQSSubscriber:
    """Test suite for SQSSubscriber lifecycle and metadata extraction."""

    @pytest.mark.asyncio
    async def test_subscribe_raises_when_not_initialized(
        self, sqs_config: SQSConfig
    ) -> None:
        """Test that subscribe raises RuntimeError before the client is initialized.

        **Why this test is important:**
          - Subscribing without initialization would poll with a None client
          - Fail-fast prevents silent consumption failures and missed messages
          - Service startup ordering bugs are caught immediately

        **What it tests:**
          - RuntimeError with "not initialized" message is raised
        """
        sub = SQSSubscriber(sqs_config)

        async def handler(msg: Message) -> None:
            pass

        with pytest.raises(RuntimeError, match="not initialized"):
            await sub.subscribe("topic", handler)

    @pytest.mark.asyncio
    async def test_close_stops_the_polling_loop(
        self, sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that close stops a running subscribe loop after the current poll.

        **Why this test is important:**
          - Graceful shutdown requires the polling loop to stop cleanly
          - Kubernetes sends SIGTERM before killing pods; the subscriber must stop polling
          - Failure to close would cause the pod to be force-killed and lose in-flight messages

        **What it tests:**
          - close() during a poll makes subscribe() return after exactly one receive
        """
        async with SQSSubscriber(sqs_config) as subscriber:

            async def receive(**_kwargs: object) -> dict[str, object]:
                await subscriber.close()
                return {"Messages": []}

            aws_client.receive_message.side_effect = receive

            async def handler(msg: Message) -> None:
                pass

            await subscriber.subscribe("topic", handler)

        aws_client.receive_message.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_extract_metadata(
        self, sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that a delivered message carries its SQS MessageAttributes as a flat dict.

        **Why this test is important:**
          - Message metadata carries routing information (topic, version) for consumers
          - Incorrect parsing would misroute messages or lose versioning context
          - The flat dict format is expected by all downstream message handlers

        **What it tests:**
          - Metadata dict contains "topic" with value "events"
          - Metadata dict contains "version" with value "1"
        """
        raw: dict[str, object] = {
            "MessageId": "m-1",
            "Body": "{}",
            "ReceiptHandle": "r-1",
            "MessageAttributes": {
                "topic": {"DataType": "String", "StringValue": "events"},
                "version": {"DataType": "String", "StringValue": "1"},
            },
        }
        metadata = await _metadata_of(sqs_config, aws_client, raw)
        assert metadata == {"topic": "events", "version": "1"}

    @pytest.mark.asyncio
    async def test_extract_metadata_empty(
        self, sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that a message with no attributes is delivered with empty metadata.

        **Why this test is important:**
          - Messages without attributes are valid in SQS and must not cause KeyError
          - The empty dict allows callers to safely iterate or check metadata
          - This edge case occurs with legacy publishers that do not set attributes

        **What it tests:**
          - Result equals an empty dict for a message with no MessageAttributes
        """
        raw: dict[str, object] = {
            "MessageId": "m-1",
            "Body": "{}",
            "ReceiptHandle": "r-1",
        }
        assert await _metadata_of(sqs_config, aws_client, raw) == {}
