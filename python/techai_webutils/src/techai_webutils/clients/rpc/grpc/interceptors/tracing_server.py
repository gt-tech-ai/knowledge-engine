"""gRPC server interceptor for distributed tracing.

Extracts the inbound W3C trace context (traceparent) from gRPC request metadata
and starts a SERVER span as its child, so a trace started upstream (e.g. an HTTP
request through Kong, or a Connect call from a Go service) continues into the
Python gRPC handler instead of starting a new, disconnected trace.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

import grpc
from opentelemetry import trace as otel_trace
from opentelemetry.propagate import extract
from opentelemetry.trace import SpanKind, Status, StatusCode
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

_RESPONSE_PROPAGATOR = TraceContextTextMapPropagator()
"""W3C TraceContext propagator used directly (not the global one) so the ``traceresponse`` value is
deterministic regardless of process-wide propagator setup — mirroring the Go connect interceptor."""


def _set_trace_response(context: grpc.aio.ServicerContext) -> None:
    """Return the active span's trace id to the caller as ``traceresponse`` trailing metadata.

    The gRPC analogue of the Go connect interceptor's ``traceresponse`` header: serialize
    the current span context to W3C traceparent format and attach it as trailing metadata so a caller
    can correlate the RPC to the spans/metrics/logs it produced. No-op when there is no valid span.
    """
    carrier: dict[str, str] = {}
    _RESPONSE_PROPAGATOR.inject(carrier)
    traceparent = carrier.get("traceparent")
    if traceparent:
        context.set_trailing_metadata((("traceresponse", traceparent),))


class TracingServerInterceptor(grpc.aio.ServerInterceptor):  # type: ignore[misc]
    """Async server interceptor that continues the inbound trace per unary RPC."""

    def __init__(self, service_name: str) -> None:
        """Acquire the OTel tracer whose instrumentation-scope name is ``service_name``."""
        self._tracer = otel_trace.get_tracer(service_name)

    async def intercept_service(
        self,
        continuation: Callable[[grpc.HandlerCallDetails], Awaitable[Any]],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> Any:  # noqa: ANN401  (grpc handler type)
        """Wrap unary-unary AND unary-stream handlers in a SERVER span parented on inbound context.

        The server-streaming case matters here: the reference RPC ``RetrievalService/QueryStream`` is
        server-streaming, so a unary-only interceptor would leave the entire retrieval query path
        (rewrite → retrieve → citations → generate) un-parented — each inner client span would start a
        NEW root trace, breaking single-request correlation. Wrapping the whole streamed response in one
        SERVER span keeps that span active across every ``yield``, so every inner client-stack span is
        its child and shares the upstream trace id.
        """
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler

        method = handler_call_details.method or "unknown"
        carrier = dict(handler_call_details.invocation_metadata or [])
        parent = extract(carrier)
        tracer = self._tracer

        if handler.unary_unary is not None:
            inner_unary = handler.unary_unary

            async def traced_unary(request: object, context: grpc.aio.ServicerContext) -> object:
                """Run the wrapped unary handler inside a SERVER span (failure -> ERROR status)."""
                with tracer.start_as_current_span(method, context=parent, kind=SpanKind.SERVER) as span:
                    span.set_attribute("rpc.system", "grpc")
                    span.set_attribute("rpc.method", method)
                    _set_trace_response(context)
                    try:
                        return await inner_unary(request, context)
                    except Exception as exc:
                        span.record_exception(exc)
                        span.set_status(Status(StatusCode.ERROR))
                        raise

            return grpc.unary_unary_rpc_method_handler(
                traced_unary,
                request_deserializer=handler.request_deserializer,
                response_serializer=handler.response_serializer,
            )

        if handler.unary_stream is not None:
            inner_stream = handler.unary_stream

            async def traced_stream(request: object, context: grpc.aio.ServicerContext) -> Any:  # noqa: ANN401
                """Run the wrapped server-streaming handler inside one SERVER span held open across all yields.

                The span stays current for the whole stream, so the rewrite/retrieve/citations/generate
                client spans that fire while producing chunks are children of this one — the single
                request stays one trace. A failure is recorded on the span (status ERROR) and re-raised.
                """
                with tracer.start_as_current_span(method, context=parent, kind=SpanKind.SERVER) as span:
                    span.set_attribute("rpc.system", "grpc")
                    span.set_attribute("rpc.method", method)
                    _set_trace_response(context)
                    try:
                        async for response in inner_stream(request, context):
                            yield response
                    except Exception as exc:
                        span.record_exception(exc)
                        span.set_status(Status(StatusCode.ERROR))
                        raise

            return grpc.unary_stream_rpc_method_handler(
                traced_stream,
                request_deserializer=handler.request_deserializer,
                response_serializer=handler.response_serializer,
            )

        # Other shapes (stream_unary / stream_stream) are not used by our services; pass through untraced.
        return handler
