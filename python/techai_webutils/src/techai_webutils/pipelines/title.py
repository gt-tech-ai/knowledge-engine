"""LLM-backed conversation title generator: (question, answer) -> a short title.

Retrieval-free — a plain LLM completion mirroring ``pipelines/rewriter.py``. The **prompt is
dependency-injected** (a ``TitlePrompt``) so this generic pipeline carries no product-specific prompt
text; the consuming app supplies the prompt strategy at its composition root.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol

from techai_webutils.core.interfaces.retrieval import TitleGenerator

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.llm import LLMMessage, LLMProvider

# Cap the persisted title so a runaway completion can't bloat the conversation title column.
_MAX_TITLE_LEN = 80
"""Maximum characters retained in a generated conversation title before truncation."""


class TitlePrompt(Protocol):
    """Builds the title LLM messages from the (question, answer) pair (DI'd by the consumer)."""

    def messages(self, query: str, answer: str) -> list[LLMMessage]:
        """Return the LLM messages instructing the model to title the (query, answer) exchange."""
        ...


def _sanitize_title(raw: str) -> str:
    """Normalize a raw LLM title: collapse to one line, drop surrounding quotes, cap length.

    LLMs wrap titles in quotes or add line breaks / verbosity; this keeps the persisted title clean
    and short. A blank/whitespace completion collapses to "" (the caller's skip signal).
    """
    title = " ".join(raw.split())  # collapse all whitespace (incl. newlines); trims both ends
    title = title.strip("\"'")  # drop surrounding quotes/apostrophes — balanced OR lopsided
    return title[:_MAX_TITLE_LEN].rstrip()


class LlmTitleGenerator(TitleGenerator):
    """Generates a short conversation title via an LLM + an injected prompt strategy."""

    def __init__(self, llm: LLMProvider, prompt: TitlePrompt) -> None:
        """Bind the LLM provider and the (product-specific) title prompt strategy."""
        self._llm = llm
        self._prompt = prompt

    async def generate_title(self, query: str, answer: str) -> str:
        """Return a sanitized short title for the (query, answer); "" when the model returns nothing."""
        response = await self._llm.complete(self._prompt.messages(query, answer))
        return _sanitize_title(response.content)
