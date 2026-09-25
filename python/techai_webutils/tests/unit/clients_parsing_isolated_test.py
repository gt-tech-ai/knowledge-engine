"""Tests for process-isolated parsing (timeout-kill, worker-error, real parse)."""

import multiprocessing as mp
import os
import resource
import time
from pathlib import Path

import pymupdf
import pytest

from techai_webutils.clients.parsing.isolated.isolated import (
    IsolatedParser,
    IsolationError,
    _apply_memory_limit,
    _parse_document,
    _worker,
    run_isolated,
)
from techai_webutils.core.domain import DocumentFormat

# Module-level task functions so the spawn start-method can pickle them into the child.


def _double(value: int) -> int:
    """Return twice the input (fast, well-behaved task)."""
    return value * 2


def _boom() -> None:
    """Raise inside the worker to exercise error propagation."""
    msg = "kaboom"
    raise ValueError(msg)


def _sleep_forever() -> str:
    """Overrun any short timeout so the parent must kill the worker."""
    time.sleep(30)
    return "never"


def _large_result() -> str:
    """Return a payload well over the ~64KB IPC pipe buffer (exercises get-before-join)."""
    return "x" * (256 * 1024)  # 256 KiB


def _exit_without_result() -> None:
    """Exit the worker abruptly without a queue put (simulates a segfault / OOM-kill)."""
    os._exit(0)  # noqa: SLF001 - deliberate hard exit that bypasses the result put


def _make_pdf(text: str) -> bytes:
    """Build a one-page PDF containing ``text``."""
    doc = pymupdf.open()
    doc.new_page().insert_text((72, 72), text)
    return doc.tobytes()


class TestRunIsolated:
    def test_returns_worker_result(self) -> None:
        """Test that a well-behaved task's result is returned from the subprocess.

        **Why this test is important:**
          - Isolation must be transparent for the happy path; if results didn't round-trip, every
            parse would appear to fail.

        **What it tests:**
          - run_isolated(_double, 21) returns 42.
        """
        assert run_isolated(_double, 21, timeout_seconds=30) == 42

    def test_timeout_kills_worker_and_raises(self) -> None:
        """Test that an overrunning worker is killed and TimeoutError is raised promptly.

        **Why this test is important:**
          - A runaway parser must not hang the consumer; the timeout+kill is the hard bound that
            keeps one document from stalling ingestion.

        **What it tests:**
          - A 30s-sleeping task under a 0.4s timeout raises TimeoutError in well under the sleep.
        """
        start = time.monotonic()
        with pytest.raises(TimeoutError):
            run_isolated(_sleep_forever, timeout_seconds=0.4)
        assert time.monotonic() - start < 10.0

    def test_worker_error_becomes_isolation_error(self) -> None:
        """Test that a task exception surfaces as IsolationError (not a silent success).

        **Why this test is important:**
          - A parser crash must be distinguishable from success so the document is failed, not
            marked indexed with empty content.

        **What it tests:**
          - A raising task yields IsolationError carrying the message.
        """
        with pytest.raises(IsolationError, match="kaboom"):
            run_isolated(_boom, timeout_seconds=30)

    def test_large_result_round_trips(self) -> None:
        """Test that a result larger than the IPC pipe buffer returns intact, not as a false timeout.

        **Why this test is important:**
          - A child that put()s a >64KB result cannot exit until the parent drains the queue; a
            parent that join()s before get() would deadlock and then spuriously time out on any
            real-sized document. This pins the get-before-join contract that the fix depends on.

        **What it tests:**
          - run_isolated returning a 256 KiB payload completes well within the timeout and returns
            the full payload.
        """
        start = time.monotonic()
        result = run_isolated(_large_result, timeout_seconds=30)
        assert len(result) == 256 * 1024
        assert time.monotonic() - start < 10.0

    def test_dead_worker_without_result_raises_promptly(self) -> None:
        """Test that a worker that exits without a result surfaces IsolationError, without hanging.

        **Why this test is important:**
          - A parser that segfaults / gets OOM-killed leaves no result; the parent must detect the
            dead child and fail that one document quickly, not block the whole (up to 60s) timeout.

        **What it tests:**
          - A worker that `os._exit(0)`s (no queue put) raises IsolationError well within the
            timeout window.
        """
        start = time.monotonic()
        with pytest.raises(IsolationError, match="died without a result"):
            run_isolated(_exit_without_result, timeout_seconds=30)
        assert time.monotonic() - start < 10.0


class TestWorkerEntrypoints:
    """Cover the child-process entry functions in-process (spawned code is invisible to coverage)."""

    def test_parse_document_entry_parses(self) -> None:
        """Test the module-level parse entry produces a ParsedDocument.

        **Why this test is important:**
          - This is the function the subprocess runs; if it were wrong, every isolated parse would
            fail — and subprocess code is not measured by coverage, so it needs a direct test.

        **What it tests:**
          - _parse_document parses HTML to ok Markdown.
        """
        result = _parse_document(b"<html><h1>Hi</h1></html>", "", "p.html")
        assert result.ok
        assert "# Hi" in result.markdown_content

    def test_worker_puts_result_on_queue(self) -> None:
        """Test that the worker puts a success tuple on the queue.

        **What it tests:**
          - _worker(_double, (5,)) enqueues ("ok", 10).
        """
        queue: mp.Queue = mp.get_context("spawn").Queue()  # type: ignore[type-arg]
        _worker(_double, (5,), 0, queue)
        assert queue.get(timeout=5) == ("ok", 10)

    def test_worker_puts_error_on_queue(self) -> None:
        """Test that a raising task is captured as an ("err", ...) tuple, not a crash.

        **What it tests:**
          - _worker(_boom) enqueues an error tuple carrying the message.
        """
        queue: mp.Queue = mp.get_context("spawn").Queue()  # type: ignore[type-arg]
        _worker(_boom, (), 0, queue)
        status, payload = queue.get(timeout=5)
        assert status == "err"
        assert "kaboom" in payload

    def test_worker_applies_memory_limit_when_positive(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that the worker applies the memory cap before running the task when one is set.

        **Why this test is important:**
          - The RLIMIT_AS cap is what bounds a runaway parser's allocation to a single document; if
            the worker skipped applying it, an OOM would take the pod, not one document.

        **What it tests:**
          - _worker with memory_bytes > 0 calls _apply_memory_limit with that value, then still
            enqueues the result. (Patched so the test process isn't itself capped.)
        """
        applied: list[int] = []
        monkeypatch.setattr(
            "techai_webutils.clients.parsing.isolated.isolated._apply_memory_limit",
            applied.append,
        )
        queue: mp.Queue = mp.get_context("spawn").Queue()  # type: ignore[type-arg]
        _worker(_double, (3,), 512, queue)
        assert applied == [512]
        assert queue.get(timeout=5) == ("ok", 6)

    def test_apply_memory_limit_sets_when_supported(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that the memory limit is applied as an (n, n) RLIMIT_AS when the platform allows.

        **What it tests:**
          - _apply_memory_limit(n) calls setrlimit with (n, n).
        """
        captured: dict[str, tuple[int, int]] = {}
        monkeypatch.setattr(resource, "setrlimit", lambda _res, limits: captured.setdefault("l", limits))
        _apply_memory_limit(456)
        assert captured["l"] == (456, 456)

    def test_apply_memory_limit_is_best_effort(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that a platform rejecting RLIMIT_AS is swallowed (best-effort).

        **Why this test is important:**
          - macOS rejects RLIMIT_AS; the cap must degrade to a no-op there, never crash the parse.

        **What it tests:**
          - A setrlimit that raises OSError does not propagate.
        """

        def _raise(_res: int, _limits: tuple[int, int]) -> None:
            raise OSError

        monkeypatch.setattr(resource, "setrlimit", _raise)
        _apply_memory_limit(456)  # must not raise


class TestIsolatedParser:
    @pytest.mark.asyncio
    async def test_parses_pdf_in_subprocess(self) -> None:
        """Test that IsolatedParser parses a real document end-to-end in a subprocess.

        **Why this test is important:**
          - This proves the isolation wiring (pickling, subprocess parse, result return) works with
            the real MarkItDown/pymupdf parser, not just trivial tasks.

        **What it tests:**
          - A real PDF parses ok through the subprocess with the PDF format + content preserved.
        """
        parser = IsolatedParser(timeout_seconds=60)
        result = await parser.parse(_make_pdf("Isolated content"), filename="doc.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert "Isolated" in result.markdown_content

    @pytest.mark.asyncio
    async def test_parse_timeout_becomes_failed_document(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Test that a subprocess timeout maps to a failed ParsedDocument, not a raise.

        **Why this test is important:**
          - A parse overrun must fail the single document (so the batch continues), tagged with the
            detected format so the caller can route it — never propagate out of the parser.

        **What it tests:**
          - When run_isolated raises TimeoutError, parse() returns not-ok with error 'parse_timeout'
            and the format still detected.
        """

        def _timeout(*_args: object, **_kwargs: object) -> object:
            raise TimeoutError

        monkeypatch.setattr("techai_webutils.clients.parsing.isolated.isolated.run_isolated", _timeout)
        result = await IsolatedParser(timeout_seconds=1).parse(b"%PDF-1.7\n", filename="x.pdf")
        assert not result.ok
        assert result.error == "parse_timeout"
        assert result.document_format is DocumentFormat.PDF

    @pytest.mark.asyncio
    async def test_parse_isolation_failure_becomes_failed_document(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Test that a worker crash (IsolationError) maps to a failed ParsedDocument carrying the reason.

        **Why this test is important:**
          - A crashed/OOM-killed worker must degrade to a per-document failure with a diagnosable
            reason, not take down the parse loop.

        **What it tests:**
          - When run_isolated raises IsolationError, parse() returns not-ok with the reason folded
            into 'parse_isolation_failed: ...'.
        """

        def _iso(*_args: object, **_kwargs: object) -> object:
            raise IsolationError("worker gone")

        monkeypatch.setattr("techai_webutils.clients.parsing.isolated.isolated.run_isolated", _iso)
        result = await IsolatedParser().parse(b"<html></html>", filename="x.html")
        assert not result.ok
        assert "parse_isolation_failed" in result.error
        assert "worker gone" in result.error

    @pytest.mark.asyncio
    async def test_parse_path_parses_pdf_in_subprocess(self, tmp_path: Path) -> None:
        """Test that IsolatedParser.parse_path parses a real PDF from disk in a subprocess.

        **Why this test is important:**
          - The large-document lane hands the isolated parser a PATH (not pickled bytes), so the
            whole file never crosses the process boundary in memory; this proves the path wiring
            (pickle the path, subprocess parse_path, result return) works with the real parser.

        **What it tests:**
          - A real PDF written to disk parses ok through the subprocess via parse_path, PDF format +
            content preserved.
        """
        pdf = tmp_path / "doc.pdf"
        pdf.write_bytes(_make_pdf("Isolated path content"))
        result = await IsolatedParser(timeout_seconds=60).parse_path(str(pdf), filename="doc.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert "Isolated" in result.markdown_content
