"""Unit tests for ``LLMProvider.stream_with_usage`` and its ``StreamUsage`` terminal item.

Streamed generations carry token usage only at the end of the stream (Bedrock's ``metadata`` event,
Ollama's final ``done`` chunk). ``stream_with_usage`` surfaces it as the last item so token metrics
are not empty for streamed traffic, while ``stream()`` stays the text-only API.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from typing import Any
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from techai_webutils.clients.llm.ollama import OllamaLlmProvider
from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.core.interfaces.llm import (
    LLMConfig,
    LLMMessage,
    LLMProvider,
    LLMResponse,
    StreamUsage,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource


def _messages(user: str) -> list[LLMMessage]:
    """A system + user message pair."""
    return [LLMMessage(role="system", content="be brief"), LLMMessage(role="user", content=user)]


async def _collect(iterator: AsyncIterator[Any]) -> list[Any]:
    """Drain an async iterator into a list."""
    return [item async for item in iterator]


async def _events(events: list[dict[str, Any]]) -> AsyncIterator[dict[str, Any]]:
    """Yield Converse stream events in order (the shape aiobotocore's EventStream yields)."""
    for event in events:
        yield event


def _ollama_client(lines: list[str]) -> MagicMock:
    """An ``httpx.AsyncClient`` mock whose ``stream()`` context yields the given NDJSON lines."""

    class _StreamCtx:
        async def __aenter__(self) -> _StreamCtx:
            return self

        async def __aexit__(self, *_: object) -> bool:
            return False

        def raise_for_status(self) -> None:
            return None

        async def aiter_lines(self) -> AsyncIterator[str]:
            for line in lines:
                yield line

    client = MagicMock(spec=httpx.AsyncClient)
    client.stream = MagicMock(return_value=_StreamCtx())
    return client


@pytest.mark.asyncio
async def test_bedrock_stream_with_usage_yields_usage_last():
    """Test that Bedrock's streamed Converse ends with the ``metadata`` usage as a ``StreamUsage``.

    **Why this test is important:**
      - Bedrock reports streamed token usage only in the final ``metadata`` event; dropping it
        leaves every streamed answer with zero tokens in metrics and cost.

    **What it tests:**
      - the text deltas come first, in order
      - the last item is ``StreamUsage(model, 12, 3, "end_turn")`` from ``metadata.usage`` and
        ``messageStop.stopReason``
    """
    from techai_webutils.clients.llm.bedrock import BedrockLlmProvider

    runtime = MagicMock()
    runtime.converse_stream = AsyncMock(
        return_value={
            "stream": _events(
                [
                    {"messageStart": {"role": "assistant"}},
                    {"contentBlockDelta": {"delta": {"text": "Hello"}}},
                    {"contentBlockDelta": {"delta": {"text": " world"}}},
                    {"messageStop": {"stopReason": "end_turn"}},
                    {"metadata": {"usage": {"inputTokens": 12, "outputTokens": 3, "totalTokens": 15}}},
                ]
            )
        }
    )
    client_cm = MagicMock()
    client_cm.__aenter__ = AsyncMock(return_value=runtime)
    client_cm.__aexit__ = AsyncMock(return_value=None)
    session = MagicMock()
    session.create_client = MagicMock(return_value=client_cm)

    with patch("aiobotocore.session.get_session", return_value=session):
        provider = BedrockLlmProvider(region="us-east-1", model="amazon.nova-lite-v1:0")
        items = await _collect(await provider.stream_with_usage(_messages("hi")))

    assert items == [
        "Hello",
        " world",
        StreamUsage(
            model="amazon.nova-lite-v1:0", input_tokens=12, output_tokens=3, finish_reason="end_turn"
        ),
    ]


@pytest.mark.asyncio
async def test_ollama_stream_with_usage_yields_usage_last():
    """Test that Ollama's streamed chat ends with the ``done`` chunk's counts as a ``StreamUsage``.

    **Why this test is important:**
      - Ollama puts ``prompt_eval_count`` / ``eval_count`` only on the final ``done`` chunk; the
        local stack's token panels depend on it.

    **What it tests:**
      - the content deltas come first, then ``StreamUsage("llama3.2", 7, 2, "stop")``
    """
    client = _ollama_client(
        [
            '{"model":"llama3.2","message":{"content":"Ish"},"done":false}',
            '{"model":"llama3.2","message":{"content":"mael"},"done":false}',
            '{"model":"llama3.2","message":{"content":""},"done":true,"done_reason":"stop",'
            '"prompt_eval_count":7,"eval_count":2}',
        ]
    )

    items = await _collect(
        await OllamaLlmProvider(client, model="llama3.2").stream_with_usage(_messages("hi"))
    )

    assert items == [
        "Ish",
        "mael",
        StreamUsage(model="llama3.2", input_tokens=7, output_tokens=2, finish_reason="stop"),
    ]


@pytest.mark.asyncio
async def test_stub_stream_with_usage_yields_usage_last():
    """Test that the stub streams its echo and then a fixed, deterministic ``StreamUsage``.

    **Why this test is important:**
      - Dev and CI run the stub; the streaming usage path must be exercisable with zero infra.

    **What it tests:**
      - tokens of the last user message, then ``StreamUsage("stub-llm", 4, 2, "stop")`` (word counts)
    """
    items = await _collect(await StubLlmProvider().stream_with_usage(_messages("alpha beta")))

    assert items == [
        "alpha ",
        "beta ",
        StreamUsage(model="stub-llm", input_tokens=4, output_tokens=2, finish_reason="stop"),
    ]


@pytest.mark.asyncio
async def test_stream_filters_out_usage():
    """Test that ``stream()`` on a usage-aware provider yields only text.

    **Why this test is important:**
      - Every existing caller of ``stream()`` concatenates strings; a ``StreamUsage`` leaking into
        it would corrupt the streamed answer.

    **What it tests:**
      - the stub's and Ollama's ``stream()`` yield exactly the text deltas, no ``StreamUsage``
    """
    client = _ollama_client(
        [
            '{"message":{"content":"ok"},"done":false}',
            '{"message":{"content":""},"done":true,"prompt_eval_count":1,"eval_count":1}',
        ]
    )

    stub_items = await _collect(await StubLlmProvider().stream(_messages("alpha beta")))
    ollama_items = await _collect(await OllamaLlmProvider(client, model="m").stream(_messages("hi")))

    assert stub_items == ["alpha ", "beta "]
    assert ollama_items == ["ok"]


class _TextOnlyProvider(NoOpAsyncResource, LLMProvider):
    """A provider implementing only the abstract API, as a third-party provider would."""

    async def complete(self, messages: list[LLMMessage], config: LLMConfig | None = None) -> LLMResponse:
        raise NotImplementedError

    async def stream(self, messages: list[LLMMessage], config: LLMConfig | None = None) -> AsyncIterator[str]:
        async def _gen() -> AsyncIterator[str]:
            yield "a"
            yield "b"

        return _gen()

    def model_name(self) -> str:
        return "text-only"


@pytest.mark.asyncio
async def test_default_stream_with_usage_yields_no_usage():
    """Test that the base ``stream_with_usage`` re-yields ``stream()`` and adds no usage.

    **Why this test is important:**
      - Providers written before ``stream_with_usage`` existed must keep working unchanged; the
        default must not invent usage numbers.

    **What it tests:**
      - a provider overriding only ``stream()`` yields exactly ``["a", "b"]`` from ``stream_with_usage``
    """
    items = await _collect(await _TextOnlyProvider().stream_with_usage(_messages("x")))

    assert items == ["a", "b"]
