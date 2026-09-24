"""Tests for the gRPC TracingServerInterceptor's server-streaming span lifecycle."""

from __future__ import annotations

from typing import TYPE_CHECKING
from unittest.mock import MagicMock, patch

import grpc
import pytest
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter

from techai_webutils.clients.rpc.grpc.interceptors.tracing_server import TracingServerInterceptor

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_TRACER_PATH = "techai_webutils.clients.rpc.grpc.interceptors.tracing_server.otel_trace.get_tracer"


def _mock_span_tracer() -> tuple[MagicMock, MagicMock]:
    """A mock OTel tracer whose start_as_current_span yields a mock span via a mock context manager.

    Returns (tracer, cm) so a test can assert the span context was exited (``cm.__exit__`` called ⇒
    ``span.end()``) and inspect the span.
    """
    span = MagicMock(name="span")
    cm = MagicMock(name="span_cm")
    cm.__enter__.return_value = span
    cm.__exit__.return_value = False  # do not suppress the exception that unwinds the with-block
    tracer = MagicMock(name="tracer")
    tracer.start_as_current_span.return_value = cm
    return tracer, cm


def _stream_handler() -> grpc.RpcMethodHandler[object, object]:
    """A unary-stream handler that yields chunks forever, so a test can cancel it mid-stream."""

    async def endless(_request: object, _context: object) -> AsyncIterator[str]:
        index = 0
        while True:
            yield f"chunk-{index}"
            index += 1

    handler = MagicMock(spec=grpc.RpcMethodHandler)
    handler.unary_unary = None
    handler.unary_stream = endless
    handler.request_deserializer = None
    handler.response_serializer = None
    return handler


class TestTracingServerInterceptorStreaming:
    @pytest.mark.asyncio
    async def test_server_span_closed_when_stream_cancelled_midway(self) -> None:
        """A client that cancels mid-stream still drives the SERVER span's with-block to exit (span ended).

        Why this test is important:
          - The SERVER span is held open across the whole server-streaming response via a ``with`` block.
            If grpc.aio abandons the generator on a mid-stream client cancel (aclose/GC finalisation)
            rather than driving it to StopAsyncIteration, a naive implementation could leak the span. This
            locks in that the span is always ended — and, because cancellation is not an error, that it is
            NOT recorded as ERROR.

        What it tests:
          - After the first yielded chunk the span is still open; calling ``aclose()`` on the response
            generator (the client-cancel path) exits the with-block (span ended) and does not set an
            ERROR status.
        """
        tracer, cm = _mock_span_tracer()
        span = cm.__enter__.return_value
        with patch(_TRACER_PATH, return_value=tracer):
            interceptor = TracingServerInterceptor("test")

        async def continuation(_details: object) -> grpc.RpcMethodHandler[object, object]:
            return _stream_handler()

        details = MagicMock(spec=grpc.HandlerCallDetails)
        details.method = "/test.RetrievalService/QueryStream"
        details.invocation_metadata = []

        wrapped = await interceptor.intercept_service(continuation, details)
        gen = wrapped.unary_stream(object(), MagicMock())

        assert await anext(gen) == "chunk-0"  # first chunk drove the handler into the span
        assert not cm.__exit__.called  # span still open mid-stream

        await gen.aclose()  # client cancels + abandons the stream (aclose → GeneratorExit)

        assert cm.__exit__.called  # the with-block exited → span.end() ran
        span.set_status.assert_not_called()  # a cancel is not an error


def _real_span_tracer() -> tuple[object, InMemorySpanExporter]:
    """A real OTel tracer backed by an in-memory exporter, so a test sees genuine span contexts."""
    provider = TracerProvider()
    exporter = InMemorySpanExporter()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    return provider.get_tracer("test"), exporter


class TestTracingServerInterceptorTraceResponse:
    @pytest.mark.asyncio
    async def test_unary_returns_traceresponse_trailing_metadata(self) -> None:
        """A unary RPC returns the active span's trace id to the caller as ``traceresponse`` trailing metadata.

        Why this test is important:
          - A telemetry round-trip proof needs the caller to learn its trace id;
            for the internal gRPC servers that means trailing metadata — the gRPC analogue of the Go
            connect ``traceresponse`` header. Without it an internal call cannot be correlated to its spans.

        What it tests:
          - After the handler runs, ``set_trailing_metadata`` carries a ``traceresponse`` in W3C
            traceparent format whose trace id equals the SERVER span's trace id.
        """
        tracer, exporter = _real_span_tracer()
        with patch(_TRACER_PATH, return_value=tracer):
            interceptor = TracingServerInterceptor("test")

        async def inner_unary(_request: object, _context: object) -> str:
            return "ok"

        handler = MagicMock(spec=grpc.RpcMethodHandler)
        handler.unary_unary = inner_unary
        handler.unary_stream = None
        handler.request_deserializer = None
        handler.response_serializer = None

        async def continuation(_details: object) -> grpc.RpcMethodHandler[object, object]:
            return handler

        details = MagicMock(spec=grpc.HandlerCallDetails)
        details.method = "/test.TestService/Do"
        details.invocation_metadata = []

        wrapped = await interceptor.intercept_service(continuation, details)
        context = MagicMock()
        result = await wrapped.unary_unary(object(), context)

        assert result == "ok"
        context.set_trailing_metadata.assert_called_once()
        metadata = dict(context.set_trailing_metadata.call_args[0][0])
        traceparent = metadata["traceresponse"]
        assert traceparent.startswith("00-")

        spans = exporter.get_finished_spans()
        assert len(spans) == 1
        span_context = spans[0].context
        assert span_context is not None
        span_trace_id = format(span_context.trace_id, "032x")
        assert span_trace_id in traceparent
