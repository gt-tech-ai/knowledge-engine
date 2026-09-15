"""Tests for the Python Job + EventHandler decorator stacks.

Parity with Go's ``pkg/go/tests/clients/jobevent_stack_test.go``: the event stack skips a
duplicate (D-40) and dead-letters a message that still fails after retries (D-34); the job stack
runs only on the leader (D-30).
"""

from __future__ import annotations

import pytest
from techai_webutils.clients.decorators.event_stack import EventStackDeps, wrap_handler
from techai_webutils.clients.decorators.job_stack import JobStackDeps, wrap_job
from techai_webutils.core.errors.errors import UnavailableError
from techai_webutils.core.interfaces.messaging import Message
from techai_webutils.foundation.resilience.dedup import MemoryDeduplicator
from techai_webutils.foundation.resilience.dlq import DeadLetterQueue, StubDeadLetterBackend
from techai_webutils.foundation.resilience.leader import AlwaysLeader


class _NotLeader:
    """A ``LeaderElector`` that never holds leadership (for the leader-gating test)."""

    async def is_leader(self) -> bool:
        """Never the leader."""
        return False


def _message(message_id: str = "m1") -> Message:
    """Build a minimal ``document.uploaded``-shaped message for the stack tests."""
    return Message(id=message_id, topic="t", payload=b"x")


@pytest.mark.asyncio
async def test_wrap_handler_dedup_skips_duplicate() -> None:
    """The event stack runs a redelivered message's handler exactly once (D-40).

    Why this test is important:
        - Under at-least-once delivery the same message can arrive twice; the dedup layer is the
          guard, and re-running the handler would double-process (e.g. duplicate side effects).

    What it tests:
        - Two handler invocations for the same message id run the inner handler exactly once.
    """
    calls = 0

    async def inner(_msg: Message) -> None:
        nonlocal calls
        calls += 1

    handler = wrap_handler(inner, EventStackDeps(dedup=MemoryDeduplicator()))
    msg = _message()

    await handler(msg)
    await handler(msg)

    assert calls == 1, "the handler must run exactly once for a duplicate delivery"


@pytest.mark.asyncio
async def test_wrap_handler_retries_then_dead_letters() -> None:
    """A message that still fails after retries is dead-lettered and acked (D-34).

    Why this test is important:
        - A poison message that neither dead-letters nor acks would loop forever; retrying the
          transient failure then routing it to the DLQ is how the consumer makes progress without
          losing the message.

    What it tests:
        - An always-failing (transient) handler is retried up to the configured attempts and then
          recorded in the dead-letter backend, with no exception escaping (the message is acked).
    """
    backend = StubDeadLetterBackend()
    attempts = 0

    async def inner(_msg: Message) -> None:
        nonlocal attempts
        attempts += 1
        raise UnavailableError("downstream down")

    handler = wrap_handler(
        inner,
        EventStackDeps(dlq=DeadLetterQueue(backend), retry_max_attempts=2),
    )

    await handler(_message())  # must not raise -- the failure is dead-lettered

    assert attempts == 2, "the transient failure is retried before dead-lettering"
    assert len(backend.letters) == 1
    assert backend.letters[0].id == "m1"


@pytest.mark.asyncio
async def test_wrap_job_leader_gating() -> None:
    """The job stack runs the job only on the leader, so an N-replica schedule fires once (D-30).

    Why this test is important:
        - Running a scheduled job on every replica would duplicate its effect (e.g. N reaper
          sweeps); leader election is the guard that makes it fire once.

    What it tests:
        - A non-leader instance skips the job (never runs it); the leader runs it exactly once.
    """
    follower_calls = 0
    leader_calls = 0

    async def follower_job() -> None:
        nonlocal follower_calls
        follower_calls += 1

    async def leader_job() -> None:
        nonlocal leader_calls
        leader_calls += 1

    await wrap_job(follower_job, JobStackDeps(leader=_NotLeader()))()
    assert follower_calls == 0, "a non-leader must not run the job"

    await wrap_job(leader_job, JobStackDeps(leader=AlwaysLeader()))()
    assert leader_calls == 1, "the leader runs the job once"
