"""Env-aware LLM provider factory (foundation/logger ``NewFromConfig`` pattern).

No Bedrock LLM exists locally, so dev selects the stub and stage/prod the real Bedrock client from
config (``LlmConfig.kind``). Unknown kinds fail loudly. The real Bedrock client is imported lazily so
the dev/stub path never loads the AWS SDK.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.llm.stub import StubLlmProvider

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.llm import LLMProvider

# Default generation model — Claude Sonnet 4.6 on Bedrock, invoked via its cross-region inference
# profile. Every Anthropic model on Bedrock is inference-profile-only (a bare foundation-model id
# AccessDenies at runtime), so the default is the `us.anthropic.*` profile id, not a bare/retired id.
# This is the single canonical default — config.py's `retrieval_llm_model` Field imports it.
DEFAULT_MODEL = "us.anthropic.claude-sonnet-4-6"
"""Default generation model — the Claude Sonnet Bedrock cross-region inference profile id."""

# Default rewrite model — Claude Haiku 4.5 on Bedrock (its inference profile): rewriting a follow-up
# into a standalone query is a cheap, latency-sensitive step, so it uses the smaller/faster model
# ('s per-step selection).
DEFAULT_REWRITE_MODEL = "us.anthropic.claude-haiku-4-5-20251001-v1:0"
"""Default follow-up rewrite model — the cheaper/faster Claude Haiku Bedrock inference profile."""


class LlmKind(StrEnum):
    """Which LLM provider implementation to build."""

    STUB = "stub"
    """The deterministic echo provider (dev/local; no Bedrock)."""
    OLLAMA = "ollama"
    """The local Ollama generator (dev/local; real answers via the same Ollama that serves embeddings)."""
    BEDROCK = "bedrock"
    """The real AWS Bedrock (Anthropic Claude) provider (stage/prod)."""


@dataclass(frozen=True, slots=True)
class LlmConfig:
    """LLM client configuration resolved from ``retrieval.llm.*``."""

    kind: LlmKind = LlmKind.STUB
    """Selects the provider implementation (``stub`` in dev, ``bedrock`` in stage/prod)."""
    region: str = "us-east-1"
    """AWS region for the Bedrock runtime client (unused by the stub)."""
    endpoint: str | None = None
    """Optional Bedrock endpoint override; ``None`` uses AWS default resolution."""
    model: str = DEFAULT_MODEL
    """Bedrock model id to invoke (defaults to Claude Sonnet)."""
    fallback_model: str = ""
    """Fallback Bedrock model id; when set on a bedrock provider, throttle/5xx retries here.
    Empty disables the fallback (a bare provider is returned)."""
    host: str = "http://localhost:11434"
    """Ollama server base URL for the ``ollama`` kind (unused by stub/bedrock)."""
    timeout_seconds: float = 300.0
    """Per-request HTTP timeout for the ``ollama`` kind — CPU generation is slow and httpx defaults to 5s."""


def new_llm_from_config(config: LlmConfig) -> LLMProvider:
    """Build the LLMProvider selected by ``config.kind`` (stub, ollama, or bedrock)."""
    if config.kind is LlmKind.STUB:
        return StubLlmProvider()
    if config.kind is LlmKind.OLLAMA:
        # Lazy import: httpx client + provider are only built for the local Ollama path. The model must
        # be an Ollama chat model (e.g. "llama3.2"), NOT the Bedrock DEFAULT_MODEL — config sets it.
        import httpx  # noqa: PLC0415

        from techai_webutils.clients.llm.ollama import OllamaLlmProvider  # noqa: PLC0415

        client = httpx.AsyncClient(base_url=config.host, timeout=config.timeout_seconds)
        return OllamaLlmProvider(client, config.model)
    if config.kind is LlmKind.BEDROCK:
        # A bedrock provider with no model id defers failure to first invocation, where it surfaces as
        # an opaque Bedrock validation/AccessDenied error. Fail loudly at construction instead (audit R9)
        # — the same loud-misconfiguration contract as the unknown-kind guard below.
        if not config.model:
            msg = "bedrock llm kind requires a model id, got empty"
            raise ValueError(msg)
        # Lazy import: keep aiobotocore off the dev/stub path (loaded only in stage/prod).
        from techai_webutils.clients.llm.bedrock import BedrockLlmProvider  # noqa: PLC0415

        primary = BedrockLlmProvider(region=config.region, model=config.model, endpoint=config.endpoint)
        if not config.fallback_model:
            return primary
        # Wrap with the throttle/5xx fallback chain; the decorator is only loaded here.
        from techai_webutils.clients.llm.decorators import FallbackLlmProvider  # noqa: PLC0415

        fallback = BedrockLlmProvider(
            region=config.region, model=config.fallback_model, endpoint=config.endpoint
        )
        return FallbackLlmProvider(primary, fallback)
    msg = f"unknown llm kind: {config.kind!r}"
    raise ValueError(msg)
