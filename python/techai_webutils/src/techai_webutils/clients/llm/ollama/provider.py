"""Ollama LLM provider — local dev text generation via the Ollama ``/api/chat`` endpoint.

The local analogue of the Bedrock LLM: instead of AWS, it generates against a local Ollama server
(the same one that serves embeddings), so the RAG generator produces real answers with no external
dependency. Selected by config (``retrieval.llm.kind = ollama``). Takes an injected
``httpx.AsyncClient`` (base_url = the Ollama host) so unit tests inject a mock; the factory that
builds it owns the client's lifetime (process-lived — see ``new_llm_from_config``).

Wire shape: ``POST /api/chat`` with ``{"model", "messages": [{"role","content"}], "stream", "options"}``
returns ``{"message": {"content"}, "done", "done_reason", "prompt_eval_count", "eval_count"}`` for a
non-streaming call, or newline-delimited JSON chunks (each ``{"message": {"content"}, "done"}``) when
streaming.
"""

from __future__ import annotations

import json
from typing import TYPE_CHECKING

import httpx

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.llm import LLMConfig, LLMProvider, LLMResponse
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from techai_webutils.core.interfaces.llm import LLMMessage


def _finish_reason(done_reason: str | None) -> str:
    """Map Ollama's ``done_reason`` to the LLMResponse ``finish_reason`` vocabulary.

    Ollama reports ``length`` when it hit ``num_predict`` and ``stop`` otherwise (or ``None`` on older
    servers); anything that isn't an explicit length cap is treated as a normal stop.
    """
    return "length" if done_reason == "length" else "stop"


def _to_app_error(exc: Exception) -> AppError:
    """Map an Ollama transport/decode failure to a coded ``AppError`` (ARCHITECTURE.md#error-codes).

    Raw ``httpx`` / JSON errors must not cross the provider boundary: the resilience decorators (retry,
    circuit breaker) and the transport status mapping derive their behaviour from the ``ErrorCode``, so
    an unwrapped ``httpx.HTTPStatusError`` / ``ValueError`` would bypass transient-vs-terminal
    classification. A timeout → ``TIMEOUT``; an unreachable server or a 5xx/429 → ``UNAVAILABLE``
    (transient, retryable); a 4xx or a malformed body → ``INTERNAL`` (terminal).
    """
    if isinstance(exc, httpx.TimeoutException):
        return AppError(ErrorCode.TIMEOUT, f"ollama chat timed out: {exc}", cause=exc)
    if isinstance(exc, httpx.HTTPStatusError):
        status = exc.response.status_code
        code = ErrorCode.UNAVAILABLE if status >= 500 or status == 429 else ErrorCode.INTERNAL  # noqa: PLR2004
        return AppError(code, f"ollama chat returned HTTP {status}", cause=exc)
    if isinstance(exc, httpx.RequestError):
        return AppError(ErrorCode.UNAVAILABLE, f"ollama chat unreachable: {exc}", cause=exc)
    return AppError(ErrorCode.INTERNAL, f"ollama chat decode error: {exc}", cause=exc)


class OllamaLlmProvider(NoOpAsyncResource, LLMProvider):
    """``LLMProvider`` backed by a local Ollama server (``/api/chat``).

    ``LLMConfig`` (temperature / max_tokens / top_p / stop) is mapped onto Ollama's ``options``; an
    explicit ``config.model`` overrides the provider's default model for a single call.
    """

    def __init__(self, client: httpx.AsyncClient, model: str) -> None:
        """Bind the httpx client (base_url = Ollama host) and the default chat model."""
        self._client = client
        self._model = model

    def _chat_payload(
        self, messages: list[LLMMessage], config: LLMConfig | None, *, stream: bool
    ) -> dict[str, object]:
        """Build the /api/chat request body from the messages + LLMConfig (options mapping)."""
        cfg = config or LLMConfig()
        options: dict[str, object] = {
            "temperature": cfg.temperature,
            "num_predict": cfg.max_tokens,
            "top_p": cfg.top_p,
        }
        if cfg.stop_sequences:
            options["stop"] = cfg.stop_sequences
        return {
            "model": cfg.model or self._model,
            "messages": [{"role": m.role, "content": m.content} for m in messages],
            "stream": stream,
            "options": options,
        }

    async def complete(self, messages: list[LLMMessage], config: LLMConfig | None = None) -> LLMResponse:
        """Generate a single completion for the message history (one non-streaming /api/chat call).

        Transport/decode failures are wrapped in a coded ``AppError`` (``_to_app_error``) so a raw
        ``httpx`` / JSON error never crosses the provider boundary.
        """
        try:
            response = await self._client.post(
                "/api/chat", json=self._chat_payload(messages, config, stream=False)
            )
            response.raise_for_status()
            body = response.json()
        except (httpx.HTTPError, ValueError) as exc:
            raise _to_app_error(exc) from exc
        message = body.get("message") or {}
        return LLMResponse(
            content=message.get("content", ""),
            model=body.get("model", self._model),
            input_tokens=int(body.get("prompt_eval_count", 0)),
            output_tokens=int(body.get("eval_count", 0)),
            finish_reason=_finish_reason(body.get("done_reason")),
        )

    async def stream(self, messages: list[LLMMessage], config: LLMConfig | None = None) -> AsyncIterator[str]:
        """Stream a completion token-by-token. Awaited to obtain the iterator, then ``async for``."""
        return self._stream_tokens(self._chat_payload(messages, config, stream=True))

    async def _stream_tokens(self, payload: dict[str, object]) -> AsyncIterator[str]:
        """Yield content deltas from the streamed /api/chat NDJSON until ``done``.

        Transport/decode failures are wrapped in a coded ``AppError`` (``_to_app_error``); a
        ``GeneratorExit`` from client cancellation propagates untouched.
        """
        try:
            async with self._client.stream("POST", "/api/chat", json=payload) as response:
                response.raise_for_status()
                async for line in response.aiter_lines():
                    if not line.strip():
                        continue
                    chunk = json.loads(line)
                    token = (chunk.get("message") or {}).get("content", "")
                    if token:
                        yield token
                    if chunk.get("done"):
                        return
        except (httpx.HTTPError, ValueError) as exc:
            raise _to_app_error(exc) from exc

    def model_name(self) -> str:
        """Return the Ollama chat model identifier."""
        return self._model
