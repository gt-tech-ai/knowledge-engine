"""``AiSpanEnricher`` — stamps GenAI semantic attributes on the current span (ARCHITECTURE.md#decorator-order).

A ``__getattr__`` proxy in the ``TracingProxy`` style that wraps an ``LLMProvider`` or a
``RetrievalEngine``. It opens no span of its own: the composition root already runs each step inside a
span (the step pipeline's), so the enricher stamps that span with what only the client boundary
knows — the model, the token usage and the finish reason of a generation, or the shape of a
retrieval. One mechanism covers both ``complete`` and the streamed path (``stream`` consumes the
provider's ``stream_with_usage`` and stamps when the terminal ``StreamUsage`` arrives).

Attributes (the contract dashboards read):

- LLM: ``gen_ai.system``, ``gen_ai.step``, ``gen_ai.request.model``, ``gen_ai.usage.input_tokens``,
  ``gen_ai.usage.output_tokens``, ``gen_ai.response.finish_reason``.
- Retrieval: ``gen_ai.step``, ``retrieval.top_k``, ``retrieval.result_count``,
  ``retrieval.document_ids`` (the first 20 ids, comma-joined).
- Content (only when ``capture_content``): span events ``gen_ai.content.prompt`` (attribute
  ``gen_ai.prompt``, the joined message contents) and ``gen_ai.content.completion`` (attribute
  ``gen_ai.completion``), each PII-redacted (``redact_pii``) and capped at 4 KiB.
"""

from __future__ import annotations

import functools
import inspect
from types import MappingProxyType
from typing import TYPE_CHECKING, cast

from opentelemetry import trace

from techai_webutils.core.interfaces.llm import StreamUsage
from techai_webutils.core.interfaces.retrieval import RetrievalEngine
from techai_webutils.foundation.logger.logger import get_logger
from techai_webutils.foundation.logger.redact import redact_pii

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Callable, Mapping

    from opentelemetry.trace import Span

    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage, LLMResponse
    from techai_webutils.core.interfaces.retrieval import RetrievalResult

GEN_AI_SYSTEM_BY_PROVIDER: Mapping[str, str] = MappingProxyType(
    {
        "BedrockLlmProvider": "aws.bedrock",
        "FallbackLlmProvider": "aws.bedrock",
        "OllamaLlmProvider": "ollama",
        "StubLlmProvider": "stub",
    }
)
"""``gen_ai.system`` value per provider class name (the fallback decorator only ever wraps Bedrock)."""

_UNKNOWN_SYSTEM = "unknown"
"""``gen_ai.system`` for a provider class not in ``GEN_AI_SYSTEM_BY_PROVIDER``."""

_MAX_DOCUMENT_IDS = 20
"""Most document ids stamped on ``retrieval.document_ids`` (keeps the attribute under span limits)."""

_MAX_CONTENT_BYTES = 4096
"""Cap, in UTF-8 bytes, on a captured prompt or completion event attribute."""

_RETRIEVE_SIGNATURE = inspect.signature(RetrievalEngine.retrieve)
"""The ``RetrievalEngine.retrieve`` contract signature, used to resolve the effective ``top_k``."""


def _cap(text: str) -> str:
    """Redact PII in ``text`` and truncate it to ``_MAX_CONTENT_BYTES`` UTF-8 bytes (never mid-character)."""
    encoded = redact_pii(text).encode("utf-8")[:_MAX_CONTENT_BYTES]
    return encoded.decode("utf-8", errors="ignore")


def _messages_arg(args: tuple[object, ...], kwargs: dict[str, object]) -> list[LLMMessage]:
    """Return the ``messages`` argument of an ``LLMProvider`` call (positional or keyword)."""
    return cast("list[LLMMessage]", args[0] if args else kwargs.get("messages", []))


class AiSpanEnricher:
    """Proxy that stamps GenAI attributes on the current span around an LLM or retrieval call.

    ``complete``, ``stream`` and ``retrieve`` are enriched; every other attribute (``model_name``,
    the async-context methods, plain values) passes through untouched. Enrichment is best-effort:
    any failure while stamping is logged at warning (with the step) and the call's own result is
    returned untouched.
    """

    def __init__(
        self,
        inner: object,
        *,
        step: str,
        capture_content: bool,
    ) -> None:
        """Wrap ``inner`` (an ``LLMProvider`` or ``RetrievalEngine``) for the pipeline step ``step``.

        Args:
            inner: The provider or engine to wrap.
            step: The pipeline step name stamped as ``gen_ai.step`` (``rewrite``, ``generate``, …).
            capture_content: Whether prompt and completion text are recorded as span events.

        """
        self._inner = inner
        self._step = step
        self._capture_content = capture_content
        self._system = GEN_AI_SYSTEM_BY_PROVIDER.get(type(inner).__name__, _UNKNOWN_SYSTEM)
        # Resolved by name like LoggingProxy: structlog is configured once at the process root.
        self._logger = get_logger("ai_enricher")

    def __getattr__(self, name: str) -> object:
        """Return the inner attribute, enriched when it is ``complete``, ``stream`` or ``retrieve``."""
        attr = getattr(self._inner, name)
        if not callable(attr):
            return attr
        wrap: Callable[[Callable[..., object]], object] | None = {
            "complete": self._wrap_complete,
            "stream": self._wrap_stream,
            "retrieve": self._wrap_retrieve,
        }.get(name)
        return attr if wrap is None else wrap(attr)

    def _safely(self, enrich: Callable[..., None], *args: object) -> None:
        """Run one enrichment step; log a failure at warning and never let it reach the caller."""
        try:
            enrich(*args)
        except Exception:
            self._logger.warning("ai span enrichment failed", step=self._step, exc_info=True)

    def _stamp_llm(self, span: Span, model: str, input_tokens: int, output_tokens: int, finish: str) -> None:
        """Stamp the GenAI request/usage attributes of one model call on ``span``."""
        span.set_attribute("gen_ai.system", self._system)
        span.set_attribute("gen_ai.step", self._step)
        span.set_attribute("gen_ai.request.model", model)
        span.set_attribute("gen_ai.usage.input_tokens", input_tokens)
        span.set_attribute("gen_ai.usage.output_tokens", output_tokens)
        span.set_attribute("gen_ai.response.finish_reason", finish)

    def _capture(self, span: Span, messages: list[LLMMessage], completion: str) -> None:
        """Add the redacted, capped prompt and completion events to ``span`` when capture is on."""
        if not self._capture_content:
            return
        span.add_event(
            "gen_ai.content.prompt", {"gen_ai.prompt": _cap("\n".join(m.content for m in messages))}
        )
        span.add_event("gen_ai.content.completion", {"gen_ai.completion": _cap(completion)})

    def _wrap_complete(self, attr: Callable[..., object]) -> object:
        """Wrap ``complete``: await it, then stamp the response's model, usage and finish reason."""

        @functools.wraps(attr)
        async def complete(*args: object, **kwargs: object) -> LLMResponse:
            span = trace.get_current_span()
            response: LLMResponse = await attr(*args, **kwargs)  # type: ignore[misc]

            def enrich() -> None:
                config: LLMConfig | None = args[1] if len(args) > 1 else kwargs.get("config")  # type: ignore[assignment]
                model = config.model if config is not None and config.model else response.model
                self._stamp_llm(
                    span, model, response.input_tokens, response.output_tokens, response.finish_reason
                )
                self._capture(span, _messages_arg(args, kwargs), response.content)

            self._safely(enrich)
            return response

        return complete

    def _wrap_stream(self, attr: Callable[..., object]) -> object:
        """Wrap ``stream``: consume ``stream_with_usage``, re-yield text, stamp usage at exhaustion."""
        source = getattr(self._inner, "stream_with_usage", attr)

        @functools.wraps(attr)
        async def stream(*args: object, **kwargs: object) -> AsyncIterator[str]:
            # The span current when the stream is OPENED is the step's span; it is captured now and
            # stamped later, because by exhaustion another span may be current.
            span = trace.get_current_span()
            items: AsyncIterator[str | StreamUsage] = await source(*args, **kwargs)  # type: ignore[misc]
            return self._relay(items, span, _messages_arg(args, kwargs))

        return stream

    async def _relay(
        self, items: AsyncIterator[str | StreamUsage], span: Span, messages: list[LLMMessage]
    ) -> AsyncIterator[str]:
        """Yield the text deltas of ``items``; stamp ``span`` when the terminal ``StreamUsage`` arrives.

        The completion text is accumulated only when content capture is on, and captured once the
        stream is exhausted.
        """
        parts: list[str] = []
        async for item in items:
            if isinstance(item, StreamUsage):
                self._safely(
                    self._stamp_llm,
                    span,
                    item.model,
                    item.input_tokens,
                    item.output_tokens,
                    item.finish_reason,
                )
                continue
            if self._capture_content:
                parts.append(item)
            yield item
        self._safely(self._capture, span, messages, "".join(parts))

    def _wrap_retrieve(self, attr: Callable[..., object]) -> object:
        """Wrap ``retrieve``: await it, then stamp top_k, the result count and the first 20 document ids."""

        @functools.wraps(attr)
        async def retrieve(*args: object, **kwargs: object) -> list[RetrievalResult]:
            span = trace.get_current_span()
            results: list[RetrievalResult] = await attr(*args, **kwargs)  # type: ignore[misc]

            def enrich() -> None:
                # Bind against the contract (not the wrapped callable) so the default top_k is
                # resolved the same way whichever engine is wrapped; ``None`` stands in for ``self``.
                bound = _RETRIEVE_SIGNATURE.bind(None, *args, **kwargs)
                bound.apply_defaults()
                span.set_attribute("gen_ai.step", self._step)
                span.set_attribute("retrieval.top_k", int(bound.arguments["top_k"]))  # type: ignore[arg-type]
                span.set_attribute("retrieval.result_count", len(results))
                span.set_attribute(
                    "retrieval.document_ids", ",".join(r.document_id for r in results[:_MAX_DOCUMENT_IDS])
                )

            self._safely(enrich)
            return results

        return retrieve
