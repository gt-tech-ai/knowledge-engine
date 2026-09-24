"""Tests for server interceptor builder and graceful server."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import grpc
import pytest

from techai_webutils.clients.rpc.grpc.interceptors.auth import AuthServerInterceptor
from techai_webutils.clients.rpc.grpc.interceptors.server_builder import (
    ServerInterceptorBuilder,
    _LoggingServerInterceptor,
    _MetricsServerInterceptor,
    _RecoveryServerInterceptor,
    _ServiceAuthServerInterceptor,
)
from techai_webutils.clients.rpc.grpc.interceptors.tracing_server import TracingServerInterceptor
from techai_webutils.clients.transport.grpc.server import GracefulServer, ServerConfig


class TestServerInterceptorBuilder:
    """Test suite for server-side interceptor builder."""

    def test_empty_build(self) -> None:
        """Test that a builder with no interceptors configured produces an empty chain.

        **Why this test is important:**
          - A lightweight internal service may want no server-side interceptors at all
          - The builder must not silently inject defaults the operator did not ask for
          - Establishes the baseline against which the additive with_* methods are measured

        **What it tests:**
          - build() returns an empty list when nothing is configured
        """
        interceptors = ServerInterceptorBuilder().build()
        assert interceptors == []

    def test_with_auth(self) -> None:
        """Test that with_auth adds the auth-claims extraction interceptor.

        **Why this test is important:**
          - Server-side identity extraction is what makes gateway-set claims available to handlers
          - If the builder failed to add it, every handler would run without an identity
          - Confirms the auth interceptor is wired through the builder API

        **What it tests:**
          - build() returns exactly one interceptor
          - It is an AuthServerInterceptor instance
        """
        interceptors = ServerInterceptorBuilder().with_auth().build()
        assert len(interceptors) == 1
        assert isinstance(interceptors[0], AuthServerInterceptor)

    @pytest.mark.asyncio
    async def test_with_auth_uses_the_consumers_header_mapping(self) -> None:
        """with_auth(headers=...) builds an auth interceptor that reads the consumer's metadata keys.

        **Why this test is important:**
          - Services build their interceptor stack through this builder; a header mapping it drops
            would leave every builder-built server on the default header contract.

        **What it tests:**
          - The built interceptor extracts the user id from the custom ``x-sub`` key.
        """
        from techai_webutils.clients.rpc.grpc.interceptors.auth import (
            HeaderClaimMapping,
            _auth_claims_var,
            get_auth_claims,
        )

        [interceptor] = (
            ServerInterceptorBuilder().with_auth(headers=HeaderClaimMapping(user_id="x-sub")).build()
        )
        token = _auth_claims_var.set(None)
        try:
            await interceptor.intercept_service(
                AsyncMock(return_value="handler"), MagicMock(invocation_metadata=[("x-sub", "u-1")])
            )
            claims = get_auth_claims()
            assert claims is not None
            assert claims.user_id == "u-1"
        finally:
            _auth_claims_var.reset(token)

    def test_with_logging(self) -> None:
        """Test that with_logging adds a logging interceptor bound to the given logger.

        **Why this test is important:**
          - Per-RPC server logs are the primary trace for diagnosing inbound requests
          - A missing logging interceptor would leave a blind spot in server observability
          - Confirms the builder instantiates and adds the logging interceptor

        **What it tests:**
          - build() returns exactly one interceptor
          - It is a _LoggingServerInterceptor instance
        """
        logger = MagicMock()
        interceptors = ServerInterceptorBuilder().with_logging(logger).build()  # type: ignore[arg-type]
        assert len(interceptors) == 1
        assert isinstance(interceptors[0], _LoggingServerInterceptor)

    def test_with_recovery(self) -> None:
        """Test that with_recovery adds the panic-recovery interceptor.

        **Why this test is important:**
          - The recovery interceptor converts unhandled exceptions into clean INTERNAL responses
          - Without it, a handler panic could crash the request or leak a stack trace to clients
          - Confirms the recovery interceptor is wired through the builder API

        **What it tests:**
          - build() returns exactly one interceptor
          - It is a _RecoveryServerInterceptor instance
        """
        interceptors = ServerInterceptorBuilder().with_recovery().build()
        assert len(interceptors) == 1
        assert isinstance(interceptors[0], _RecoveryServerInterceptor)

    def test_full_chain_ordering(self) -> None:
        """Test that a full chain is ordered recovery (outermost) → logging → auth (innermost).

        **Why this test is important:**
          - Interceptor order is behavioral: recovery must wrap everything to catch all panics,
            and auth must run last so logging records the request even on auth failures
          - Building the chain in the wrong order would silently change error and logging semantics
          - The builder must impose a fixed, correct order regardless of call sequence

        **What it tests:**
          - build() returns exactly three interceptors
          - The order is _RecoveryServerInterceptor, _LoggingServerInterceptor, AuthServerInterceptor
        """
        logger = MagicMock()
        interceptors = (
            ServerInterceptorBuilder()
            .with_recovery()
            .with_logging(logger)  # type: ignore[arg-type]
            .with_auth()
            .build()
        )
        assert len(interceptors) == 3
        assert isinstance(interceptors[0], _RecoveryServerInterceptor)
        assert isinstance(interceptors[1], _LoggingServerInterceptor)
        assert isinstance(interceptors[2], AuthServerInterceptor)


class TestMetricsServerInterceptor:
    """Behaviour of the metrics interceptor: per-RPC execution count, duration, and errors."""

    @staticmethod
    def _details(method: str = "/svc/M") -> object:
        """Handler-call-details carrying the RPC method name (the metric label)."""
        return MagicMock(method=method)

    @pytest.mark.asyncio
    async def test_unary_records_duration_and_execution(self) -> None:
        """A successful unary RPC records an execution count + a duration observation, no error.

        **Why this test is important:**
          - Duration/error recording was reserved (only the execution counter fired); a latency
            regression on a unary RPC was invisible. This pins that the wrapped handler now times the
            call and leaves the error counter untouched on success.

        **What it tests:**
          - Invoking the wrapped unary handler returns the real result, increments ``executions`` and
            observes ``histogram`` exactly once, and never touches ``errors``.
        """
        histogram, executions, errors = MagicMock(), MagicMock(), MagicMock()
        interceptor = _MetricsServerInterceptor(histogram, executions, errors)

        async def _ok(_req: object, _ctx: object) -> str:
            return "ok"

        real = grpc.unary_unary_rpc_method_handler(_ok)
        wrapped = await interceptor.intercept_service(AsyncMock(return_value=real), self._details())

        assert await wrapped.unary_unary("req", MagicMock()) == "ok"
        executions.inc.assert_called_once()
        histogram.observe.assert_called_once()
        errors.inc.assert_not_called()

    @pytest.mark.asyncio
    async def test_unary_records_error_on_raise(self) -> None:
        """A unary handler that raises increments the error counter and still records duration.

        **Why this test is important:**
          - Error-rate is the primary health signal; if the wrapper swallowed or missed the exception
            the error counter would never move and the exception would be lost.

        **What it tests:**
          - The exception propagates, ``errors`` is incremented once, and ``histogram`` is still
            observed (finally-block timing).
        """
        histogram, executions, errors = MagicMock(), MagicMock(), MagicMock()
        interceptor = _MetricsServerInterceptor(histogram, executions, errors)

        async def _boom(_req: object, _ctx: object) -> str:
            raise RuntimeError("boom")

        real = grpc.unary_unary_rpc_method_handler(_boom)
        wrapped = await interceptor.intercept_service(AsyncMock(return_value=real), self._details())

        with pytest.raises(RuntimeError):
            await wrapped.unary_unary("req", MagicMock())
        errors.inc.assert_called_once()
        histogram.observe.assert_called_once()

    @pytest.mark.asyncio
    async def test_streaming_records_error_on_midstream_raise(self) -> None:
        """A streaming handler that raises AFTER its first yield still records the error + duration.

        **Why this test is important:**
          - QueryStream is a server-streaming RPC whose body runs as the runtime iterates the async
            generator; an exception after the first token must still be counted. A wrapper that only
            guarded the handler *call* (not the iteration) would miss it — the exact gap this closes.

        **What it tests:**
          - The already-yielded item is delivered, the mid-stream exception propagates, ``errors`` is
            incremented once, and ``histogram`` is observed once (duration across the whole stream).
        """
        histogram, executions, errors = MagicMock(), MagicMock(), MagicMock()
        interceptor = _MetricsServerInterceptor(histogram, executions, errors)

        async def _gen(_req: object, _ctx: object):  # noqa: ANN202
            yield "a"
            raise RuntimeError("mid-stream boom")

        real = grpc.unary_stream_rpc_method_handler(_gen)
        wrapped = await interceptor.intercept_service(AsyncMock(return_value=real), self._details())

        collected = []
        with pytest.raises(RuntimeError):
            async for item in wrapped.unary_stream("req", MagicMock()):
                collected.append(item)
        assert collected == ["a"]
        errors.inc.assert_called_once()
        histogram.observe.assert_called_once()


class TestRecoveryServerInterceptor:
    """The recovery interceptor converts an uncaught handler exception to INTERNAL, without masking
    an intentional ``context.abort`` (which raises ``grpc.aio.AbortError``)."""

    @staticmethod
    def _details(method: str = "/svc/M") -> object:
        """Handler-call-details carrying the RPC method name."""
        return MagicMock(method=method)

    @pytest.mark.asyncio
    async def test_unary_uncaught_exception_aborts_internal(self) -> None:
        """A unary handler raising an unexpected exception is converted to an INTERNAL abort.

        **Why this test is important:**
          - Before this, an uncaught handler exception surfaced as gRPC's opaque ``UNKNOWN``; the
            recovery interceptor must convert it to an explicit ``INTERNAL`` (ARCHITECTURE.md#error-codes, matching
            the Go recovery interceptor) so clients and dashboards see a coded server error.

        **What it tests:**
          - The wrapped handler calls ``context.abort`` once with ``StatusCode.INTERNAL``.
        """
        interceptor = _RecoveryServerInterceptor()

        async def _boom(_req: object, _ctx: object) -> str:
            raise RuntimeError("boom")

        real = grpc.unary_unary_rpc_method_handler(_boom)
        wrapped = await interceptor.intercept_service(AsyncMock(return_value=real), self._details())
        context = MagicMock()
        context.abort = AsyncMock()

        await wrapped.unary_unary("req", context)
        context.abort.assert_awaited_once()
        assert context.abort.call_args.args[0] == grpc.StatusCode.INTERNAL

    @pytest.mark.asyncio
    async def test_intentional_abort_is_not_masked(self) -> None:
        """An intentional ``context.abort`` (AbortError) propagates unchanged — NOT re-aborted INTERNAL.

        **Why this test is important:**
          - A handler that deliberately aborts (e.g. ``NOT_FOUND``/``PERMISSION_DENIED``) must keep its
            chosen status. A recovery interceptor that blindly caught every exception would rewrite it
            to ``INTERNAL`` — silently corrupting the API's error contract.

        **What it tests:**
          - The handler's ``AbortError`` propagates and the recovery interceptor does NOT call
            ``context.abort`` (it never converts an intentional abort to INTERNAL).
        """
        interceptor = _RecoveryServerInterceptor()

        async def _aborter(_req: object, _ctx: object) -> str:
            raise grpc.aio.AbortError("intended")

        real = grpc.unary_unary_rpc_method_handler(_aborter)
        wrapped = await interceptor.intercept_service(AsyncMock(return_value=real), self._details())
        context = MagicMock()
        context.abort = AsyncMock()

        with pytest.raises(grpc.aio.AbortError):
            await wrapped.unary_unary("req", context)
        context.abort.assert_not_called()

    @pytest.mark.asyncio
    async def test_streaming_midstream_exception_aborts_internal(self) -> None:
        """A streaming handler raising after its first yield is converted to an INTERNAL abort.

        **Why this test is important:**
          - QueryStream is server-streaming; a failure after the first token must still become a coded
            INTERNAL, not an opaque stream drop. Proves the recovery wrapper iterates the real
            generator (not just guards the handler call).

        **What it tests:**
          - The already-yielded item is delivered, then ``context.abort`` is called once with INTERNAL.
        """
        interceptor = _RecoveryServerInterceptor()

        async def _gen(_req: object, _ctx: object):  # noqa: ANN202
            yield "a"
            raise RuntimeError("mid-stream boom")

        real = grpc.unary_stream_rpc_method_handler(_gen)
        wrapped = await interceptor.intercept_service(AsyncMock(return_value=real), self._details())
        context = MagicMock()
        context.abort = AsyncMock()

        collected = [item async for item in wrapped.unary_stream("req", context)]
        assert collected == ["a"]
        context.abort.assert_awaited_once()
        assert context.abort.call_args.args[0] == grpc.StatusCode.INTERNAL


class TestGracefulServer:
    """Test suite for GracefulServer wrapper."""

    def test_default_config(self) -> None:
        """Test that the default ServerConfig uses the documented defaults.

        **Why this test is important:**
          - Deployments rely on the default port and shutdown timeout when none are specified.

        **What it tests:**
          - The default port is 50051 and the default shutdown_timeout is 30.0 seconds.
        """
        cfg = ServerConfig()
        assert cfg.port == 50051
        assert cfg.shutdown_timeout == 30.0

    def test_custom_config(self) -> None:
        """Test that an explicit ServerConfig retains its overrides.

        **Why this test is important:**
          - Each service binds a different port; an ignored config would clash on the default port.

        **What it tests:**
          - The configured port (8093) and max_workers (5) are retained.
        """
        cfg = ServerConfig(port=8093, shutdown_timeout=15.0, max_workers=5)
        assert cfg.port == 8093
        assert cfg.max_workers == 5

    @pytest.mark.asyncio
    async def test_set_ready(self) -> None:
        """Test that toggling the async readiness flag is safe in both directions.

        **Why this test is important:**
          - Health/readiness probes flip this flag to gate traffic during startup and shutdown; a
            toggle that raised would break the readiness signal.

        **What it tests:**
          - await set_ready(True) then await set_ready(False) completes without raising.
        """
        server = GracefulServer()
        await server.set_ready(ready=True)
        await server.set_ready(ready=False)

    @pytest.mark.asyncio
    async def test_server_property(self) -> None:
        """Test that the server property exposes a real underlying async grpc.aio.Server.

        **Why this test is important:**
          - Callers register services on this exposed server before serving; the wrong object would
            make registration silently fail.

        **What it tests:**
          - server.server is an instance of grpc.aio.Server.
        """
        import grpc

        server = GracefulServer()
        assert isinstance(server.server, grpc.aio.Server)


class TestServiceAuthInterceptor:
    """Test suite for the s2s service-token validation interceptor."""

    @staticmethod
    def _details(metadata: list[tuple[str, str]]) -> object:
        """Build handler-call-details carrying the given invocation metadata."""
        return MagicMock(invocation_metadata=metadata)

    @staticmethod
    def _handler() -> grpc.RpcMethodHandler:
        """A real unary-unary handler so the deny path can inspect its streaming shape."""
        return grpc.unary_unary_rpc_method_handler(lambda _req, _ctx: "ok")

    @pytest.mark.asyncio
    async def test_bypasses_when_no_token_configured(self) -> None:
        """Test that an empty expected token disables the check (dev bypass).

        **Why this test is important:**
          - Local/dev runs have no service token; the interceptor must let every call through
            rather than lock the service out entirely.

        **What it tests:**
          - With an empty token, intercept_service returns the real handler unchanged.
        """
        interceptor = _ServiceAuthServerInterceptor("")
        handler = self._handler()
        continuation = AsyncMock(return_value=handler)
        result = await interceptor.intercept_service(continuation, self._details([]))
        assert result is handler

    @pytest.mark.asyncio
    async def test_allows_valid_bearer_token(self) -> None:
        """Test that a matching Bearer token is authorized.

        **Why this test is important:**
          - The API service presents ``authorization: Bearer <token>``; a valid token must pass so
            legitimate internal calls succeed.

        **What it tests:**
          - With a matching token, intercept_service returns the real handler.
        """
        interceptor = _ServiceAuthServerInterceptor("secret")
        handler = self._handler()
        continuation = AsyncMock(return_value=handler)
        details = self._details([("authorization", "Bearer secret")])
        result = await interceptor.intercept_service(continuation, details)
        assert result is handler

    @pytest.mark.asyncio
    async def test_rejects_missing_or_wrong_token(self) -> None:
        """Test that a missing/incorrect token is rejected with UNAUTHENTICATED.

        **Why this test is important:**
          - This is the security boundary: a rogue in-cluster caller forging clearance must be
            rejected before the handler (and its clearance-filtered data) runs.

        **What it tests:**
          - A wrong token yields a substitute handler (not the real one), and invoking it aborts
            with UNAUTHENTICATED.
        """
        interceptor = _ServiceAuthServerInterceptor("secret")
        handler = self._handler()
        continuation = AsyncMock(return_value=handler)
        details = self._details([("authorization", "Bearer wrong")])
        result = await interceptor.intercept_service(continuation, details)

        assert result is not handler  # a deny handler was substituted
        context = MagicMock()
        context.abort = AsyncMock()
        await result.unary_unary("req", context)
        context.abort.assert_awaited_once()
        assert context.abort.call_args.args[0] == grpc.StatusCode.UNAUTHENTICATED

    def test_builder_service_auth_fails_loud_without_a_token(self) -> None:
        """Test that with_service_auth never silently drops the check.

        **Why this test is important:**
          - An empty token used to omit the interceptor, so a missing secret in a deployed
            environment left the service open to any caller; the bypass must be an explicit
            dev-only choice, not a side effect of a blank setting.

        **What it tests:**
          - with_service_auth("secret") includes a _ServiceAuthServerInterceptor.
          - with_service_auth("") raises ValueError at wiring time.
          - with_service_auth("", allow_unauthenticated=True) builds without the interceptor.
        """
        with_token = ServerInterceptorBuilder().with_service_auth("secret").build()
        assert any(isinstance(i, _ServiceAuthServerInterceptor) for i in with_token)
        with pytest.raises(ValueError, match="service token"):
            ServerInterceptorBuilder().with_service_auth("")
        bypass = ServerInterceptorBuilder().with_service_auth("", allow_unauthenticated=True).build()
        assert not any(isinstance(i, _ServiceAuthServerInterceptor) for i in bypass)


class TestServerInterceptorBuilderFullStack:
    """Covers the with_rate_limit / with_metrics / with_tracing builder slots."""

    def test_all_slots_compose_into_the_chain(self) -> None:
        """Every with_* slot (recovery, rate_limit, metrics, tracing, logging, auth)
        contributes to the built interceptor chain.

        **Why this test is important:**
          - A production server enables the full observability + resilience stack; a
            dropped slot would silently disable a cross-cutting concern. The tracing slot in
            particular was a no-op: with_tracing set a field build() never read, so the SERVER
            span was never installed and single-request trace correlation was impossible.

        **What it tests:**
          - Building with all slots yields more interceptors than the empty build, the chain is a
            list of grpc.aio.ServerInterceptor, and a TracingServerInterceptor IS present (before
            the logging interceptor, so the span wraps the request log + handler).
        """
        from unittest.mock import MagicMock

        import grpc

        limiter, histogram, logger = (MagicMock() for _ in range(3))
        interceptors = (
            ServerInterceptorBuilder()
            .with_recovery()
            .with_rate_limit(limiter)  # type: ignore[arg-type]
            .with_metrics(histogram)
            .with_tracing("svc")
            .with_logging(logger)  # type: ignore[arg-type]
            .with_service_auth("secret")
            .with_auth()
            .build()
        )
        empty = ServerInterceptorBuilder().build()
        assert len(interceptors) > len(empty)
        assert all(isinstance(i, grpc.aio.ServerInterceptor) for i in interceptors)
        types = [type(i) for i in interceptors]
        assert TracingServerInterceptor in types, types
        # Tracing sits OUTSIDE logging so the span wraps the handler + request log.
        assert types.index(TracingServerInterceptor) < types.index(_LoggingServerInterceptor)

    def test_with_tracing_adds_tracing_interceptor(self) -> None:
        """with_tracing wires a TracingServerInterceptor; omitting it leaves the chain untraced.

        **Why this test is important:**
          - This is the regression guard for the silent no-op: previously with_tracing stored a
            field build() never consumed, so no service ever got a SERVER span. The wiring must be
            observable at the builder boundary, not just "configured".

        **What it tests:**
          - with_tracing("svc") includes a TracingServerInterceptor; a builder without it does not.
        """
        with_tracing = ServerInterceptorBuilder().with_tracing("svc").build()
        assert any(isinstance(i, TracingServerInterceptor) for i in with_tracing)
        without = ServerInterceptorBuilder().build()
        assert not any(isinstance(i, TracingServerInterceptor) for i in without)
