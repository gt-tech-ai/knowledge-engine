"""Tests for the FallbackLlmProvider (Bedrock model-fallback chain).

Why these tests are important:
  - When the primary generation model (Sonnet) is throttled or returns a server error, the service
    must transparently retry the request on the cheaper fallback model (Haiku) rather than fail the
    user's query; and when both fail it must surface a clean 503-mapped error, not a raw botocore
    exception. A non-transient error (e.g. a validation 400) must NOT trigger a wasteful fallback.

What they test:
  - Fallback on a throttle (429) and on a 5xx; UnavailableError when both models fail; a non-retryable
    error is re-raised without invoking the fallback.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from unittest.mock import AsyncMock, MagicMock

import pytest
from botocore.exceptions import ClientError

from techai_webutils.clients.llm.decorators import FallbackLlmProvider
from techai_webutils.core.errors import AppError, UnavailableError
from techai_webutils.foundation.resilience.aws_boundary import botocore_error_to_app_error
from techai_webutils.core.interfaces.llm import LLMMessage, LLMResponse


async def _stream(*tokens: str) -> AsyncIterator[str]:
    """An async token stream that yields the given tokens then completes."""
    for token in tokens:
        yield token


async def _stream_then_fail(exc: Exception, *tokens: str) -> AsyncIterator[str]:
    """An async token stream that yields the given tokens then raises exc."""
    for token in tokens:
        yield token
    raise exc


def _client_error(code: str, status: int) -> ClientError:
    """Build a botocore ClientError mimicking a Bedrock throttle/server response."""
    return ClientError(
        {"Error": {"Code": code, "Message": "boom"}, "ResponseMetadata": {"HTTPStatusCode": status}},
        "Converse",
    )


def _response(model: str) -> LLMResponse:
    """A minimal LLMResponse tagged with the model that produced it."""
    return LLMResponse(content="answer", model=model, input_tokens=1, output_tokens=1, finish_reason="stop")


def _providers(primary_error: Exception, fallback_result: object) -> FallbackLlmProvider:
    """Wire a FallbackLlmProvider whose primary raises and whose fallback returns/raises the given value."""
    primary = AsyncMock()
    primary.complete.side_effect = primary_error
    fallback = AsyncMock()
    if isinstance(fallback_result, Exception):
        fallback.complete.side_effect = fallback_result
    else:
        fallback.complete.return_value = fallback_result
    return FallbackLlmProvider(primary, fallback)


_MESSAGES = [LLMMessage(role="user", content="hi")]


@pytest.mark.asyncio
async def test_fallback_on_throttle() -> None:
    """A 429 throttle on the primary routes the request to the fallback model."""
    provider = _providers(_client_error("ThrottlingException", 429), _response("haiku"))
    result = await provider.complete(_MESSAGES)
    assert result.model == "haiku"


@pytest.mark.asyncio
async def test_fallback_on_5xx() -> None:
    """A 5xx server error on the primary routes the request to the fallback model."""
    provider = _providers(_client_error("InternalServerException", 500), _response("haiku"))
    result = await provider.complete(_MESSAGES)
    assert result.model == "haiku"


@pytest.mark.asyncio
async def test_both_models_fail_raises_unavailable() -> None:
    """When the fallback also fails, a 503-mapped UnavailableError is raised (not a raw botocore error)."""
    provider = _providers(
        _client_error("ThrottlingException", 429), _client_error("ThrottlingException", 429)
    )
    with pytest.raises(UnavailableError):
        await provider.complete(_MESSAGES)


@pytest.mark.asyncio
async def test_non_retryable_error_reraises_without_fallback() -> None:
    """A non-transient error (validation 400) is re-raised and the fallback is never invoked."""
    provider = _providers(_client_error("ValidationException", 400), _response("haiku"))
    with pytest.raises(ClientError):
        await provider.complete(_MESSAGES)
    provider._fallback.complete.assert_not_awaited()  # noqa: SLF001 - assert the fallback was skipped


def _coded_provider_error(code: str, status: int) -> AppError:
    """The coded AppError a real provider raises for a botocore failure (``raise ... from exc``)."""
    client_error = _client_error(code, status)
    try:
        raise botocore_error_to_app_error(client_error, "bedrock converse") from client_error
    except AppError as err:
        return err


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("code", "status", "falls_back"),
    [
        ("ThrottlingException", 429, True),
        ("InternalServerException", 500, True),
        ("AccessDeniedException", 403, False),
    ],
)
async def test_coded_provider_error_is_classified_through_its_cause(
    code: str, status: int, *, falls_back: bool
) -> None:
    """A provider's coded AppError is classified by the botocore error it wraps.

    Why this test is important:
      - The Bedrock provider translates botocore failures into a coded ``AppError`` at its boundary,
        so the throttle/5xx a real primary raises never carries ``.response`` itself; a classifier
        that only looks at the raised exception never falls back in production.

    What it tests:
      - A throttle or 5xx wrapped as the provider wraps it routes the request to the fallback model;
        a wrapped non-transient error (access denied) re-raises without invoking the fallback.
    """
    provider = _providers(_coded_provider_error(code, status), _response("haiku"))
    if falls_back:
        assert (await provider.complete(_MESSAGES)).model == "haiku"
        return
    with pytest.raises(AppError):
        await provider.complete(_MESSAGES)
    provider._fallback.complete.assert_not_awaited()  # noqa: SLF001 - assert the fallback was skipped


@pytest.mark.asyncio
async def test_error_without_botocore_shape_reraises() -> None:
    """An exception lacking botocore's response shape is not transient and re-raises.

    Why this test is important:
      - The classifier duck-types botocore's ``response`` dict; a plain exception
        (e.g. a bug, not a throttle) must propagate, never trigger a wasteful
        fallback.

    What it tests:
      - A primary RuntimeError with no ``.response`` re-raises and the fallback is
        never invoked.
    """
    provider = _providers(RuntimeError("unexpected"), _response("haiku"))
    with pytest.raises(RuntimeError):
        await provider.complete(_MESSAGES)
    provider._fallback.complete.assert_not_awaited()  # noqa: SLF001


@pytest.mark.asyncio
async def test_async_context_enters_and_exits_both_providers() -> None:
    """The composite enters and exits both wrapped providers' async contexts.

    Why this test is important:
      - FallbackLlmProvider owns the lifecycle of both models; a leaked or
        un-entered client would break connection pooling / cleanup.

    What it tests:
      - ``async with`` enters primary then fallback, and exits both on leave.
    """
    primary, fallback = AsyncMock(), AsyncMock()
    provider = FallbackLlmProvider(primary, fallback)
    async with provider as entered:
        assert entered is provider
        primary.__aenter__.assert_awaited_once()
        fallback.__aenter__.assert_awaited_once()
    primary.__aexit__.assert_awaited_once()
    fallback.__aexit__.assert_awaited_once()


@pytest.mark.asyncio
async def test_aenter_rolls_back_primary_when_fallback_enter_fails() -> None:
    """A failure entering the fallback rolls back the already-entered primary.

    Why this test is important:
      - Python does not call __aexit__ when __aenter__ raises, so a half-opened
        composite would leak the entered primary client; the rollback prevents it.

    What it tests:
      - When the fallback's __aenter__ raises, __aenter__ propagates and the primary
        is exited (rolled back).
    """
    primary, fallback = AsyncMock(), AsyncMock()
    fallback.__aenter__.side_effect = RuntimeError("enter failed")
    provider = FallbackLlmProvider(primary, fallback)
    with pytest.raises(RuntimeError):
        await provider.__aenter__()
    primary.__aexit__.assert_awaited_once()


@pytest.mark.asyncio
async def test_stream_falls_back_before_first_token() -> None:
    """A primary stream that fails before any token falls back to the fallback stream.

    Why this test is important:
      - A throttle at stream open must not fail the user's query when the fallback
        can still serve it; only a failure before the first token is safely retryable.

    What it tests:
      - Primary stream raises a throttle before yielding → the fallback stream's
        tokens are returned.
    """
    primary, fallback = AsyncMock(), AsyncMock()
    primary.stream.return_value = _stream_then_fail(_client_error("ThrottlingException", 429))
    fallback.stream.return_value = _stream("hello", " world")
    provider = FallbackLlmProvider(primary, fallback)

    tokens = [t async for t in await provider.stream(_MESSAGES)]
    assert tokens == ["hello", " world"]


@pytest.mark.asyncio
async def test_stream_propagates_failure_after_first_token() -> None:
    """A primary stream that fails after emitting a token propagates (no fallback).

    Why this test is important:
      - Once tokens have been sent to the client, retrying on the fallback would
        duplicate output; a mid-stream failure must propagate instead.

    What it tests:
      - Primary yields one token then raises → the error propagates and the fallback
        is never streamed.
    """
    primary, fallback = AsyncMock(), AsyncMock()
    primary.stream.return_value = _stream_then_fail(_client_error("ThrottlingException", 429), "partial")
    provider = FallbackLlmProvider(primary, fallback)

    with pytest.raises(ClientError):
        _ = [t async for t in await provider.stream(_MESSAGES)]
    fallback.stream.assert_not_called()


@pytest.mark.asyncio
async def test_stream_both_fail_raises_unavailable() -> None:
    """When the fallback stream also fails, a 503-mapped UnavailableError is raised.

    Why this test is important:
      - Both-models-down must surface as a clean 503, not a raw botocore error.

    What it tests:
      - Primary fails before a token and the fallback stream also fails →
        UnavailableError.
    """
    primary, fallback = AsyncMock(), AsyncMock()
    primary.stream.return_value = _stream_then_fail(_client_error("ThrottlingException", 429))
    fallback.stream.return_value = _stream_then_fail(_client_error("InternalServerException", 500))
    provider = FallbackLlmProvider(primary, fallback)

    with pytest.raises(UnavailableError):
        _ = [t async for t in await provider.stream(_MESSAGES)]


def test_model_name_returns_primary_model() -> None:
    """model_name() reports the primary model (what the caller expects absent a fallback).

    Why this test is important:
      - Callers key on the reported model id; it must reflect the primary, not the
        fallback.

    What it tests:
      - model_name() delegates to the primary provider.
    """
    primary, fallback = AsyncMock(), AsyncMock()
    primary.model_name = MagicMock(return_value="sonnet")  # sync method, not a coroutine
    assert FallbackLlmProvider(primary, fallback).model_name() == "sonnet"
