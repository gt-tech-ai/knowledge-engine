"""Unit tests for the analytics ``Fact`` wire format and the ``FactPublisher`` tier."""

from __future__ import annotations

import asyncio
from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.facts import (
    FactPublisherConfig,
    FactPublisherKind,
    new_fact_publisher_from_config,
)
from techai_webutils.clients.facts.stub import StubFactPublisher
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.core.interfaces.messaging import MessagePublisher
from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
from techai_webutils.core.types.fact import Fact

_GOLDEN = Path(__file__).resolve().parents[4] / "testdata" / "analytics_fact.golden.json"


def _fact(i: int = 0) -> Fact:
    """The golden fact (``i == 0``) or a variant with a distinct idempotency key."""
    return Fact(
        cube="genai_calls",
        org_id="org-1",
        ts=datetime(2026, 10, 9, 12, 34, 56, 789000, tzinfo=UTC),
        dims={"provider": "aws.bedrock", "model": "nova-lite", "step": "generate", "team": "t-1"},
        measures={"tokens_in": 42, "tokens_out": 7, "duration_s": 1.25},
        idempotency_key="4bf92f3577b34da6a3ce929d0e0e4736:00f067aa0ba902b7:generate"
        + ("" if i == 0 else f"-{i}"),
    )


def _dropped_counter() -> tuple[MagicMock, MagicMock]:
    """A metrics provider whose only counter (``gen_ai_fact_dropped_total``) is returned too."""
    counter = MagicMock(spec=MetricCounter)
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter.return_value = counter
    return metrics, counter


def test_fact_json_matches_golden():
    """Test that ``Fact.to_json`` is byte-identical to the shared golden fixture.

    **Why this test is important:**
      - Python producers and the Go analytics consumer share this wire format; the Go suite
        asserts the same file, so any drift in key order, number or timestamp rendering fails one
        side before it corrupts the fact lane.

    **What it tests:**
      - sorted keys, compact separators, integral measures rendered as integers, RFC 3339 UTC
        timestamp with trailing zeros trimmed (``…56.789Z``), and ``schema`` 1
    """
    assert _fact().to_json() == _GOLDEN.read_bytes()


def test_factory_builds_stub_by_default_and_rejects_unknown_kinds():
    """Test the config-selected factory: stub by default, coded errors on misconfiguration.

    **Why this test is important:**
      - The graph must boot with zero infrastructure, and a typo'd kind or a messaging kind with
        no transport must fail at construction, not drop every fact silently.

    **What it tests:**
      - the default config builds a ``StubFactPublisher`` whose ``publish`` accepts facts
      - an unknown kind, ``messaging`` without a publisher and ``messaging`` without a queue each
        raise ``AppError(INVALID_INPUT)``
    """
    stub = new_fact_publisher_from_config(FactPublisherConfig(), publisher=None)
    stub.publish([_fact()])

    assert isinstance(stub, StubFactPublisher)
    assert isinstance(stub, FactPublisher)
    for config, publisher in (
        (FactPublisherConfig(kind="kafka"), None),
        (FactPublisherConfig(kind=FactPublisherKind.MESSAGING, queue="facts"), None),
        (FactPublisherConfig(kind=FactPublisherKind.MESSAGING), MagicMock(spec=MessagePublisher)),
    ):
        with pytest.raises(AppError) as caught:
            new_fact_publisher_from_config(config, publisher=publisher)
        assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_messaging_publisher_drops_and_counts_when_full():
    """Test that a full buffer drops the fact, counts it, and never blocks or raises.

    **Why this test is important:**
      - Facts are emitted on the generation hot path; a slow queue must cost a dropped fact, not
        user latency or an error.

    **What it tests:**
      - with ``max_buffer=2`` and the sender not started, three publishes return immediately and
        exactly one ``inc(reason="buffer_full")`` is recorded
      - a publish error drops the batch and counts ``inc(2, reason="publish_error")``
    """
    metrics, dropped = _dropped_counter()
    transport = MagicMock(spec=MessagePublisher)
    transport.publish_batch = AsyncMock(side_effect=RuntimeError("sqs down"))
    publisher = new_fact_publisher_from_config(
        FactPublisherConfig(
            kind=FactPublisherKind.MESSAGING, queue="facts", max_buffer=2, flush_interval_s=0.01
        ),
        publisher=transport,
        metrics=metrics,
    )

    publisher.publish([_fact(1), _fact(2), _fact(3)])
    assert dropped.inc.call_args_list == [((), {"reason": "buffer_full"})]

    async with publisher:
        await asyncio.sleep(0.05)

    assert dropped.inc.call_args_list[1] == ((2,), {"reason": "publish_error"})
    metrics.counter.assert_called_once_with(
        "gen_ai_fact_dropped_total", "Analytics facts dropped before publish, by reason.", ["reason"]
    )


@pytest.mark.asyncio
async def test_messaging_publisher_batches_by_ten():
    """Test that buffered facts are sent in batches of at most ten and fully drained on close.

    **Why this test is important:**
      - SQS ``SendMessageBatch`` takes at most ten entries; batching keeps the request count low,
        and a graceful close must not lose the facts still buffered.

    **What it tests:**
      - 25 buffered facts reach ``publish_batch("facts", …)`` as batches of 10, 10 and 5, in order,
        with each payload equal to the fact's ``to_json()``
    """
    transport = MagicMock(spec=MessagePublisher)
    transport.publish_batch = AsyncMock(return_value=None)
    publisher = new_fact_publisher_from_config(
        FactPublisherConfig(kind=FactPublisherKind.MESSAGING, queue="facts", flush_interval_s=0.01),
        publisher=transport,
    )
    facts = [_fact(i) for i in range(1, 26)]

    publisher.publish(facts)
    async with publisher:
        pass

    sent = [c.args for c in transport.publish_batch.await_args_list]
    assert [topic for topic, _ in sent] == ["facts", "facts", "facts"]
    assert [len(batch) for _, batch in sent] == [10, 10, 5]
    assert [payload for _, batch in sent for payload in batch] == [f.to_json() for f in facts]
