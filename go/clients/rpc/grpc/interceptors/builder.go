package interceptors

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
	"google.golang.org/grpc"
)

// ServerBuilder composes server-side gRPC interceptors in the canonical
// order: recovery → rate limit → metrics → tracing → logging.
type ServerBuilder struct {
	// logger enables structured logging; nil disables the logging interceptor.
	logger interfaces.Logger

	// metrics enables request count/duration metrics; nil disables them.
	metrics interfaces.Metrics

	// tracer enables per-RPC span creation; nil disables tracing.
	tracer interfaces.Tracer

	// limiter enables rate limiting; nil disables the rate-limit interceptor.
	limiter interfaces.RateLimiter

	// bulkhead caps concurrent in-flight requests (load shedding); nil disables it.
	bulkhead interfaces.Bulkhead

	// recovery toggles the panic-recovery interceptor (requires logger).
	recovery bool
}

// NewServerBuilder creates a new ServerBuilder with no interceptors enabled.
func NewServerBuilder() *ServerBuilder {
	return &ServerBuilder{}
}

// WithRecovery enables the panic-recovery interceptor. Requires a logger to be
// set via WithLogging.
func (b *ServerBuilder) WithRecovery() *ServerBuilder {
	b.recovery = true
	return b
}

// WithRateLimit enables rate limiting with the provided limiter.
func (b *ServerBuilder) WithRateLimit(limiter interfaces.RateLimiter) *ServerBuilder {
	b.limiter = limiter
	return b
}

// WithBulkhead enables concurrency-based load shedding: requests over the
// bulkhead's concurrency limit are rejected with ResourceExhausted, so a burst
// can't exhaust goroutines or the backing DB pool. Meant for an internal surface
// no gateway rate-limits.
func (b *ServerBuilder) WithBulkhead(bh interfaces.Bulkhead) *ServerBuilder {
	b.bulkhead = bh
	return b
}

// WithMetrics enables request count and duration histogram recording.
func (b *ServerBuilder) WithMetrics(m interfaces.Metrics) *ServerBuilder {
	b.metrics = m
	return b
}

// WithTracing enables distributed trace span creation per RPC.
func (b *ServerBuilder) WithTracing(tracer interfaces.Tracer) *ServerBuilder {
	b.tracer = tracer
	return b
}

// WithLogging enables structured request/response logging.
func (b *ServerBuilder) WithLogging(logger interfaces.Logger) *ServerBuilder {
	b.logger = logger
	return b
}

// Build assembles the interceptor chain in canonical server order and returns
// server options ready for use with grpc.NewServer. Returns nil if no
// interceptors are configured.
func (b *ServerBuilder) Build() []grpc.ServerOption {
	var chain []grpc.UnaryServerInterceptor

	// Order: recovery → rate limit → bulkhead → metrics → tracing → logging
	if b.recovery && b.logger != nil {
		chain = append(chain, RecoveryServerInterceptor(b.logger))
	}
	if b.limiter != nil {
		chain = append(chain, RateLimitServerInterceptor(b.limiter))
	}
	if b.bulkhead != nil {
		chain = append(chain, BulkheadServerInterceptor(b.bulkhead))
	}
	if b.metrics != nil {
		chain = append(chain, MetricsServerInterceptor(b.metrics))
	}
	if b.tracer != nil {
		chain = append(chain, TracingServerInterceptor(b.tracer))
	}
	if b.logger != nil {
		chain = append(chain, LoggingServerInterceptor(b.logger))
	}

	if len(chain) == 0 {
		return nil
	}
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(chain...)}
}

// clientBuilderConfig holds the interceptor config shared by ClientBuilder
// and StreamingClientBuilder, plus their common With* setters. self lets each
// setter return the concrete outer builder type (ClientBuilder or
// StreamingClientBuilder) so fluent chains keep their own Build(); only
// WithTimeout is excluded because its meaning legitimately differs between
// the two (see each type's own WithTimeout doc).
type clientBuilderConfig[T any] struct {
	// self points back at the concrete outer builder so shared With* setters can
	// return it and keep the concrete type's own Build() available in fluent chains.
	self *T

	// logger enables per-attempt request/response logging; nil disables it.
	logger interfaces.Logger

	// metrics enables per-attempt count/duration metrics; nil disables them.
	metrics interfaces.Metrics

	// tracer enables per-attempt span creation; nil disables tracing.
	tracer interfaces.Tracer

	// retrier enables automatic retry of transient failures; nil disables retry.
	retrier interfaces.Retrier

	// cb enables circuit-breaker protection; nil disables circuit breaking.
	cb interfaces.CircuitBreaker

	// timeout enables a per-call (ClientBuilder) or stream-open-only
	// (StreamingClientBuilder) deadline; zero disables the timeout
	// interceptor. Set via each outer type's own WithTimeout.
	timeout time.Duration
}

// WithRetry enables automatic retry of transient failures.
func (b *clientBuilderConfig[T]) WithRetry(retrier interfaces.Retrier) *T {
	b.retrier = retrier
	return b.self
}

// WithCircuitBreaker enables circuit-breaker protection.
func (b *clientBuilderConfig[T]) WithCircuitBreaker(cb interfaces.CircuitBreaker) *T {
	b.cb = cb
	return b.self
}

// WithMetrics enables per-attempt request count and duration recording.
func (b *clientBuilderConfig[T]) WithMetrics(m interfaces.Metrics) *T {
	b.metrics = m
	return b.self
}

// WithTracing enables distributed trace span creation per attempt.
func (b *clientBuilderConfig[T]) WithTracing(tracer interfaces.Tracer) *T {
	b.tracer = tracer
	return b.self
}

// WithLogging enables structured per-attempt request/response logging.
func (b *clientBuilderConfig[T]) WithLogging(logger interfaces.Logger) *T {
	b.logger = logger
	return b.self
}

// ClientBuilder composes client-side gRPC interceptors in the canonical
// resilience order (outermost first): metrics → circuit breaker → retry →
// timeout → tracing → logging → service auth (assembled by chain).
type ClientBuilder struct {
	// clientBuilderConfig supplies the shared interceptor config and With* setters.
	clientBuilderConfig[ClientBuilder]

	// serviceToken is the caller's service-to-service bearer token; empty
	// attaches no authorization metadata (local-dev bypass).
	serviceToken string
}

// NewClientBuilder creates a new ClientBuilder with no interceptors enabled.
func NewClientBuilder() *ClientBuilder {
	b := &ClientBuilder{}
	b.self = b
	return b
}

// WithTimeout enables a deadline on every outgoing call.
func (b *ClientBuilder) WithTimeout(d time.Duration) *ClientBuilder {
	b.timeout = d
	return b
}

// WithServiceAuth attaches the caller's service-to-service bearer token as
// `authorization: Bearer <token>` outgoing metadata on every call (Phase 1), so
// the internal server can authenticate this caller. An empty token attaches nothing
// (local dev leaves it unset and the server's stub admits the call).
func (b *ClientBuilder) WithServiceAuth(token string) *ClientBuilder {
	b.serviceToken = token
	return b
}

// Build assembles the interceptor chain in canonical client order and returns
// dial options ready for use with grpc.NewClient. Returns nil if no
// interceptors are configured.
func (b *ClientBuilder) Build() []grpc.DialOption {
	var chain []grpc.UnaryClientInterceptor
	if b.metrics != nil {
		chain = append(chain, MetricsClientInterceptor(b.metrics))
	}
	if b.cb != nil {
		chain = append(chain, CircuitBreakerClientInterceptor(b.cb))
	}
	if b.retrier != nil {
		chain = append(chain, RetryClientInterceptor(b.retrier))
	}
	if b.timeout > 0 {
		chain = append(chain, TimeoutClientInterceptor(b.timeout))
	}
	if b.tracer != nil {
		chain = append(chain, TracingClientInterceptor(b.tracer))
	}
	if b.logger != nil {
		chain = append(chain, LoggingClientInterceptor(b.logger))
	}
	if b.serviceToken != "" {
		chain = append(chain, ServiceAuthClientInterceptor(b.serviceToken))
	}
	if len(chain) == 0 {
		return nil
	}
	return []grpc.DialOption{grpc.WithChainUnaryInterceptor(chain...)}
}

// StreamingClientBuilder composes client-side stream gRPC interceptors in the
// canonical order (outermost first), with timeout outermost so one open budget
// bounds the whole retried open without leaking a deadline into the long-lived
// stream (unlike the unary builder's per-attempt, inside-retry timeout):
// timeout → metrics → circuit breaker → retry → tracing → logging.
type StreamingClientBuilder struct {
	// clientBuilderConfig supplies the shared interceptor config and With* setters.
	clientBuilderConfig[StreamingClientBuilder]
}

// NewStreamingClientBuilder creates a new StreamingClientBuilder with no interceptors enabled.
func NewStreamingClientBuilder() *StreamingClientBuilder {
	b := &StreamingClientBuilder{}
	b.self = b
	return b
}

// WithTimeout enables a deadline on stream creation (open) only — not on the
// stream's subsequent data lifetime, which can legitimately run far longer
// (e.g. a long RAG generation) than a reasonable open budget. See
// TimeoutStreamClientInterceptor.
func (b *StreamingClientBuilder) WithTimeout(d time.Duration) *StreamingClientBuilder {
	b.timeout = d
	return b
}

// Build assembles the interceptor chain in canonical client order and returns
// dial options ready for use with grpc.NewClient. Returns nil if no
// interceptors are configured.
func (b *StreamingClientBuilder) Build() []grpc.DialOption {
	var chain []grpc.StreamClientInterceptor
	// Order: timeout → metrics → circuit breaker → retry → tracing → logging
	if b.timeout > 0 {
		chain = append(chain, TimeoutStreamClientInterceptor(b.timeout))
	}
	if b.metrics != nil {
		chain = append(chain, MetricsStreamClientInterceptor(b.metrics))
	}
	if b.cb != nil {
		chain = append(chain, CircuitBreakerStreamClientInterceptor(b.cb))
	}
	if b.retrier != nil {
		chain = append(chain, RetryStreamClientInterceptor(b.retrier))
	}
	if b.tracer != nil {
		chain = append(chain, TracingStreamClientInterceptor(b.tracer))
	}
	if b.logger != nil {
		chain = append(chain, LoggingStreamClientInterceptor(b.logger))
	}

	if len(chain) == 0 {
		return nil
	}
	return []grpc.DialOption{grpc.WithChainStreamInterceptor(chain...)}
}
