"""LLM provider interface for language model interactions."""

from abc import ABC, abstractmethod
from collections.abc import AsyncIterator
from dataclasses import dataclass, field

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass
class LLMMessage:
    """A message in a conversation with an LLM."""

    role: str  # "system", "user", "assistant"
    """The speaker of the message: ``system``, ``user``, or ``assistant``."""
    content: str
    """The message text."""


@dataclass
class LLMResponse:
    """Response from an LLM completion."""

    content: str
    """The generated completion text."""
    model: str
    """Identifier of the model that produced the completion."""
    input_tokens: int
    """Tokens consumed by the prompt (for cost/usage accounting)."""
    output_tokens: int
    """Tokens produced in the completion (for cost/usage accounting)."""
    finish_reason: str  # "stop", "length", "tool_use"
    """Why generation stopped: ``stop`` (natural end), ``length`` (token cap), or ``tool_use``."""


@dataclass
class LLMConfig:
    """Configuration for LLM requests."""

    model: str = ""
    """Model to use for the request (empty selects the provider's default model)."""
    temperature: float = 0.7
    """Sampling temperature; higher values yield more random output (0.0-1.0+ range)."""
    max_tokens: int = 1024
    """Maximum number of tokens to generate in the completion."""
    top_p: float = 1.0
    """Nucleus-sampling probability mass; 1.0 disables top-p truncation."""
    stop_sequences: list[str] = field(default_factory=list)
    """Sequences that, when generated, halt the completion early."""


class LLMProvider(ManagedResource, ABC):
    """Abstract LLM provider for text generation and conversation.

    Composes ``ManagedResource`` (ARCHITECTURE.md#interface-composition): a provider may own an async SDK client
    (the Bedrock backend does), so it is an async context manager — ``__aenter__`` opens
    lazily on first use, ``__aexit__`` releases it.

    Phase 1: Single-turn completion and streaming.
    Phase 2+: Multi-turn conversations, tool use, structured output, function calling.
    """

    @abstractmethod
    async def complete(self, messages: list[LLMMessage], config: LLMConfig | None = None) -> LLMResponse:
        """Generate a completion for the given message history."""

    @abstractmethod
    async def stream(self, messages: list[LLMMessage], config: LLMConfig | None = None) -> AsyncIterator[str]:
        """Stream a completion token-by-token.

        Returns an async iterator; callers ``await`` this to obtain it, then ``async for``.
        """

    @abstractmethod
    def model_name(self) -> str:
        """Return the default model identifier for this provider."""
