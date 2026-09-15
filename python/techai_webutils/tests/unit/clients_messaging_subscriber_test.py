"""Tests for SQS subscriber with mocked aiobotocore."""

from __future__ import annotations

from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from techai_webutils.core.interfaces.messaging import Message
import pytest


class TestSQSSubscriber:
    """Test suite for SQSSubscriber lifecycle and metadata extraction."""

    @pytest.mark.asyncio
    async def test_subscribe_raises_when_not_initialized(self, sqs_config: SQSConfig) -> None:
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
    async def test_close_sets_running_false(self, sqs_config: SQSConfig) -> None:
        """Test that close stops the subscriber by setting _running to False.

        **Why this test is important:**
          - Graceful shutdown requires the polling loop to stop cleanly
          - Kubernetes sends SIGTERM before killing pods; the subscriber must stop polling
          - Failure to close would cause the pod to be force-killed and lose in-flight messages

        **What it tests:**
          - _running is False after calling close()
        """
        sub = SQSSubscriber(sqs_config)
        sub._running = True
        await sub.close()
        assert sub._running is False

    def test_extract_metadata(self) -> None:
        """Test that _extract_metadata parses SQS MessageAttributes into a flat dict.

        **Why this test is important:**
          - Message metadata carries routing information (topic, version) for consumers
          - Incorrect parsing would misroute messages or lose versioning context
          - The flat dict format is expected by all downstream message handlers

        **What it tests:**
          - Metadata dict contains "topic" with value "events"
          - Metadata dict contains "version" with value "1"
        """
        raw = {
            "MessageAttributes": {
                "topic": {"DataType": "String", "StringValue": "events"},
                "version": {"DataType": "String", "StringValue": "1"},
            }
        }
        metadata = SQSSubscriber._extract_metadata(raw)
        assert metadata == {"topic": "events", "version": "1"}

    def test_extract_metadata_empty(self) -> None:
        """Test that _extract_metadata returns an empty dict when no attributes are present.

        **Why this test is important:**
          - Messages without attributes are valid in SQS and must not cause KeyError
          - The empty dict allows callers to safely iterate or check metadata
          - This edge case occurs with legacy publishers that do not set attributes

        **What it tests:**
          - Result equals an empty dict for a message with no MessageAttributes
        """
        assert SQSSubscriber._extract_metadata({}) == {}
