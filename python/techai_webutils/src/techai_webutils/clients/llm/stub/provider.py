"""Deterministic no-Bedrock LLM provider for dev/local.

There is no Bedrock LLM locally, so dev runs this stub: it echoes the last user message so
the query rewriter (passthrough) and the streaming generator both produce deterministic,
inspectable output without AWS. Selected by config (``retrieval.llm.kind = stub``).
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.llm import LLMProvider, LLMResponse
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage

_STUB_MODEL = "stub-llm"
"""Model id the stub provider reports from ``model_name()`` (no real backend)."""


def _last_user_message(messages: list[LLMMessage]) -> str:
    """Return the content of the last user-role message (empty if none)."""
    for message in reversed(messages):
        if message.role == "user":
            return message.content
    return ""


async def _token_stream(text: str) -> AsyncIterator[str]:
    """Yield ``text`` token-by-token (whitespace-delimited, trailing space preserved)."""
    for token in text.split(" "):
        if token:
            yield f"{token} "


class StubLlmProvider(NoOpAsyncResource, LLMProvider):
    """LLMProvider that echoes the last user message (deterministic dev behavior)."""

    def __init__(self, model: str = _STUB_MODEL) -> None:
        """Bind the reported model name."""
        self._model = model

    async def complete(self, messages: list[LLMMessage], config: LLMConfig | None = None) -> LLMResponse:  # noqa: ARG002
        """Return the last user message verbatim as the completion."""
        answer = _last_user_message(messages)
        return LLMResponse(
            content=answer,
            model=self._model,
            input_tokens=sum(len(m.content.split()) for m in messages),
            output_tokens=len(answer.split()),
            finish_reason="stop",
        )

    async def stream(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None = None,  # noqa: ARG002
    ) -> AsyncIterator[str]:
        """Return an async iterator streaming the last user message token-by-token."""
        return _token_stream(_last_user_message(messages))

    def model_name(self) -> str:
        """Return the stub model identifier."""
        return self._model
