"""Tests for the LLM title generator (question + answer -> short conversation title).

Prompt-agnostic: the generator takes an injected prompt strategy, so these tests inject a spec'd mock
prompt (``create_autospec(TitlePrompt)``) and assert the generator's *logic* (LLM call + title
sanitization) independent of any product prompt.
"""

from unittest.mock import create_autospec

import pytest
from techai_webutils.core.interfaces.llm import LLMProvider, LLMResponse

from techai_webutils.pipelines.title import LlmTitleGenerator, TitlePrompt


def _resp(content: str) -> LLMResponse:
    """An LLMResponse carrying ``content``."""
    return LLMResponse(content=content, model="m", input_tokens=1, output_tokens=1, finish_reason="stop")


class TestLlmTitleGenerator:
    @pytest.mark.asyncio
    async def test_returns_stripped_title(self) -> None:
        """Test that the generator returns the LLM's title, stripped, via a single completion.

        Why this test is important:
          - The title is persisted as the conversation name; it must be exactly the model's title
            (trimmed), from one LLM call.

        What it tests:
          - generate_title calls the LLM once and returns its trimmed content.
        """
        llm = create_autospec(LLMProvider, instance=True)
        llm.complete.return_value = _resp("  Q4 Revenue Analysis  ")
        gen = LlmTitleGenerator(llm, create_autospec(TitlePrompt, instance=True))
        assert await gen.generate_title("q", "a") == "Q4 Revenue Analysis"
        llm.complete.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_empty_completion_yields_empty(self) -> None:
        """Test that a blank/whitespace completion yields "" (the caller skips the rename).

        Why this test is important:
          - A blank title must never be persisted; the empty string is the caller's skip signal.

        What it tests:
          - A whitespace-only completion -> "".
        """
        llm = create_autospec(LLMProvider, instance=True)
        llm.complete.return_value = _resp("   \n  ")
        gen = LlmTitleGenerator(llm, create_autospec(TitlePrompt, instance=True))
        assert await gen.generate_title("q", "a") == ""

    @pytest.mark.asyncio
    async def test_sanitizes_quotes_newlines_and_length(self) -> None:
        """Test that a quoted / multi-line / over-long completion is normalized before use.

        Why this test is important:
          - LLMs wrap titles in quotes or add line breaks / verbosity; a raw title would look wrong
            in the conversation list and could exceed the column's sensible length.

        What it tests:
          - Surrounding quotes stripped, newlines collapsed to one line, length capped.
        """
        llm = create_autospec(LLMProvider, instance=True)
        llm.complete.return_value = _resp('"Multi\nline   quoted ' + "x" * 100 + '"')
        gen = LlmTitleGenerator(llm, create_autospec(TitlePrompt, instance=True))
        title = await gen.generate_title("q", "a")
        assert "\n" not in title
        assert not title.startswith('"') and not title.endswith('"')
        assert len(title) <= 80

    @pytest.mark.asyncio
    async def test_strips_lopsided_quote(self) -> None:
        """Test that an unbalanced leading/trailing quote is stripped (not just a matched pair).

        Why this test is important:
          - LLMs sometimes open a title with a quote and don't close it; a lone quote must not
            reach the persisted conversation title.

        What it tests:
          - A title with only a leading quote has it stripped.
        """
        llm = create_autospec(LLMProvider, instance=True)
        llm.complete.return_value = _resp('"Unclosed Title')
        gen = LlmTitleGenerator(llm, create_autospec(TitlePrompt, instance=True))
        assert await gen.generate_title("q", "a") == "Unclosed Title"
