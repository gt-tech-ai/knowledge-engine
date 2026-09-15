"""Tests for client tracing interceptor and builder tracing support."""

from __future__ import annotations

from techai_webutils.clients.rpc.grpc.interceptors.builder import InterceptorBuilder
from techai_webutils.clients.rpc.grpc.interceptors.tracing import TracingInterceptor
from unittest.mock import MagicMock


class TestTracingInterceptor:
    """Test suite for the tracing client interceptor."""

    def test_interceptor_creation(self) -> None:
        """Test that the tracing interceptor retains the tracer it was constructed with.

        **Why this test is important:**
          - The interceptor must emit spans through the exact tracer the app configured
          - If it dropped or replaced the tracer, distributed traces would vanish or split across exporters
          - Confirms the dependency is stored, the precondition for any span emission

        **What it tests:**
          - The constructed interceptor holds the injected tracer instance
        """
        tracer = MagicMock()
        interceptor = TracingInterceptor(tracer)
        assert interceptor._tracer is tracer


class TestInterceptorBuilderTracing:
    """Test suite for tracing support in the client InterceptorBuilder."""

    def test_with_tracing(self) -> None:
        """Test that with_tracing adds a TracingInterceptor to the client chain.

        **Why this test is important:**
          - Client-side spans are how outbound RPCs get correlated into end-to-end traces
          - If the builder failed to add the interceptor, client calls would be invisible to tracing
          - Confirms the tracing interceptor is wired through the builder API

        **What it tests:**
          - build() returns exactly one interceptor
          - It is a TracingInterceptor instance
        """
        tracer = MagicMock()
        interceptors = InterceptorBuilder().with_tracing(tracer).build()
        assert len(interceptors) == 1
        assert isinstance(interceptors[0], TracingInterceptor)

    def test_tracing_in_full_chain(self) -> None:
        """Test that tracing composes alongside logging and timeout interceptors.

        **Why this test is important:**
          - Production client channels stack tracing with logging and timeout interceptors together
          - Tracing must survive composition and not be dropped when other interceptors are present
          - Confirms the builder accumulates all configured interceptors rather than overwriting

        **What it tests:**
          - build() returns exactly three interceptors when logging, tracing, and timeout are all added
          - A TracingInterceptor is present in the resulting chain
        """
        tracer = MagicMock()
        interceptors = (
            InterceptorBuilder().with_logging("test").with_tracing(tracer).with_timeout(5.0).build()
        )
        assert len(interceptors) == 3
        # Tracing interceptor should be present
        assert any(isinstance(i, TracingInterceptor) for i in interceptors)
