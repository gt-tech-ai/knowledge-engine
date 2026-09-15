package interceptors

import (
	"time"

	"connectrpc.com/connect"
	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// ServerBuilder composes server-side Connect interceptors in the canonical
// order: recovery -> rate limit -> metrics -> tracing -> logging -> service auth
// -> auth -> identity -> validate.
type ServerBuilder struct {
	// serviceValidator validates the caller's service-to-service bearer token (WithServiceAuth).
	serviceValidator interfaces.ServiceTokenValidator
	// metrics collects request count and duration when set (WithMetrics).
	metrics interfaces.Metrics
	// tracer creates a span per RPC when set (WithTracing).
	tracer interfaces.Tracer
	// limiter enforces rate limiting when set (WithRateLimit).
	limiter interfaces.RateLimiter
	// bulkhead enforces concurrency-based load shedding when set (WithBulkhead).
	bulkhead interfaces.Bulkhead
	// userContextResolver resolves the caller's identity for the identity interceptor (WithIdentity).
	userContextResolver IdentityResolver
	// logger enables structured request/response logging when set (WithLogging).
	logger interfaces.Logger
	// retryBudget caps per-request retries across the downstream chain; ≤0 disables it (WithRetryBudget).
	retryBudget int32
	// recovery enables the panic-recovery interceptor (WithRecovery).
	recovery bool
	// serviceAuth enables the service-to-service auth interceptor (WithServiceAuth).
	serviceAuth bool
	// authStub, when true, synthesizes dev claims instead of requiring real auth (local dev; WithAuth).
	authStub bool
	// serviceStub bypasses service-token validation for local dev (WithServiceAuth).
	serviceStub bool
	// serviceAudit logs the service-auth outcome but admits the call (staged rollout; WithServiceAuth).
	serviceAudit bool
	// validate enables request payload validation (WithValidation).
	validate bool
	// auth enables the auth-header extraction interceptor (WithAuth).
	auth bool
	// tenant enables the tenant-propagation interceptor (WithTenant).
	tenant bool
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
// can't exhaust goroutines or the backing DB pool. Used on the internal surface
// Kong doesn't cover (R6).
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

// WithAuth enables the auth-header extraction interceptor.
// stub must be true in local dev (auth.stub: true) so the interceptor synthesizes
// dev claims when Kong is absent; false in staging/prod.
func (b *ServerBuilder) WithAuth(stub bool) *ServerBuilder {
	b.auth = true
	b.authStub = stub
	return b
}

// WithServiceAuth enables the service-to-service authentication interceptor on the
// internal (non-Kong) mount (Phase 1): it validates the caller's
// `authorization: Bearer <token>` via the validator and attaches the caller identity,
// rejecting a missing/invalid credential with CodeUnauthenticated. It runs after logging
// and BEFORE the end-user auth interceptor. stub bypasses validation for local dev;
// audit logs the outcome but admits the call (staged rollout). The validator must be
// non-nil unless stub is set; the audit-mode log uses the logger from WithLogging.
func (b *ServerBuilder) WithServiceAuth(
	validator interfaces.ServiceTokenValidator,
	stub, audit bool,
) *ServerBuilder {
	b.serviceAuth = true
	b.serviceValidator = validator
	b.serviceStub = stub
	b.serviceAudit = audit
	return b
}

// WithIdentity enables the identity enrichment interceptor, which resolves
// the caller's internal org, teams, and accessible workspaces server-side and
// merges them into AuthClaims. It runs after WithAuth.
//
// ⚠ It inherits WithAuth's stub flag (b.authStub): in stub mode the interceptor
// injects StubInternalOrgID without calling the resolver. Enabling stub auth
// (WithAuth(true)) therefore silently short-circuits real resolution for every
// caller — keep the two consistent and never enable stub outside local dev.
func (b *ServerBuilder) WithIdentity(resolver IdentityResolver) *ServerBuilder {
	b.userContextResolver = resolver
	return b
}

// WithTenant enables the tenant-propagation interceptor, which stamps the caller's
// resolved organization onto the request context (the sql.WithVar GUC for Postgres
// RLS and entctx.WithTenant for the Ent tenant interceptor/hook). It MUST run after
// WithIdentity so the org is resolved. Enable it only on clients whose Ent client
// registers tenant enforcement (contracts/ent/tenant.Register).
func (b *ServerBuilder) WithTenant() *ServerBuilder {
	b.tenant = true
	return b
}

// WithValidation enables request payload validation.
func (b *ServerBuilder) WithValidation() *ServerBuilder {
	b.validate = true
	return b
}

// WithRetryBudget attaches a shared per-request retry budget of maxRetries at the
// server entrypoint, so every retrier in the downstream chain draws from one cap
// (R4). Zero or negative disables it.
func (b *ServerBuilder) WithRetryBudget(maxRetries int32) *ServerBuilder {
	b.retryBudget = maxRetries
	return b
}

// Build assembles the interceptor chain in canonical server order and returns
// handler options ready for use with generated Connect service constructors.
// Returns nil if no interceptors are configured.
func (b *ServerBuilder) Build() []connect.HandlerOption {
	var chain []connect.Interceptor

	// Order: recovery → budget → rate limit → bulkhead → metrics → tracing →
	// logging → service auth → auth → identity → validate (identity MUST follow auth so
	// claims are present; service auth authenticates the CALLING SERVICE on internal
	// mounts and runs before end-user auth; the budget (R4) spans the whole request and
	// load shedding (R6) is early so it rejects before the request does any work).
	if b.recovery && b.logger != nil {
		chain = append(chain, RecoveryInterceptor(b.logger))
	}
	if b.retryBudget > 0 {
		chain = append(chain, BudgetInterceptor(b.retryBudget))
	}
	if b.limiter != nil {
		chain = append(chain, RateLimitInterceptor(b.limiter))
	}
	if b.bulkhead != nil {
		chain = append(chain, BulkheadInterceptor(b.bulkhead))
	}
	if b.metrics != nil {
		chain = append(chain, MetricsInterceptor(b.metrics))
	}
	if b.tracer != nil {
		chain = append(chain, TracingInterceptor(b.tracer))
	}
	if b.logger != nil {
		chain = append(chain, NewLoggingInterceptor(b.logger))
	}
	if b.serviceAuth {
		chain = append(
			chain,
			NewServiceAuthInterceptor(
				b.serviceValidator,
				b.serviceStub,
				b.serviceAudit,
				b.logger,
			),
		)
	}
	if b.auth {
		chain = append(chain, NewAuthInterceptor(b.authStub))
	}
	if b.userContextResolver != nil {
		chain = append(
			chain,
			NewIdentityInterceptor(b.userContextResolver, b.authStub),
		)
	}
	if b.tenant {
		chain = append(chain, NewTenantInterceptor())
	}
	if b.validate {
		chain = append(chain, ValidateInterceptor())
	}

	if len(chain) == 0 {
		return nil
	}
	return []connect.HandlerOption{connect.WithInterceptors(chain...)}
}

// ClientBuilder composes client-side Connect interceptors in the canonical
// resilience order (outermost first): metrics → circuit breaker → retry →
// timeout → tracing → logging (assembled by chain).
type ClientBuilder struct {
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

	// timeout enables a per-call deadline; zero disables the timeout interceptor.
	timeout time.Duration
}

// NewClientBuilder creates a new ClientBuilder with no interceptors enabled.
func NewClientBuilder() *ClientBuilder {
	return &ClientBuilder{}
}

// WithTimeout enables a deadline on every outgoing call.
func (b *ClientBuilder) WithTimeout(d time.Duration) *ClientBuilder {
	b.timeout = d
	return b
}

// WithRetry enables automatic retry of transient failures.
func (b *ClientBuilder) WithRetry(retrier interfaces.Retrier) *ClientBuilder {
	b.retrier = retrier
	return b
}

// WithCircuitBreaker enables circuit-breaker protection.
func (b *ClientBuilder) WithCircuitBreaker(cb interfaces.CircuitBreaker) *ClientBuilder {
	b.cb = cb
	return b
}

// WithMetrics enables per-attempt request count and duration recording.
func (b *ClientBuilder) WithMetrics(m interfaces.Metrics) *ClientBuilder {
	b.metrics = m
	return b
}

// WithTracing enables distributed trace span creation per attempt.
func (b *ClientBuilder) WithTracing(tracer interfaces.Tracer) *ClientBuilder {
	b.tracer = tracer
	return b
}

// WithLogging enables structured per-attempt request/response logging.
func (b *ClientBuilder) WithLogging(logger interfaces.Logger) *ClientBuilder {
	b.logger = logger
	return b
}

// chain assembles the client interceptors in canonical resilience order,
// outermost first:
//
//	metrics → circuit breaker → retry → timeout → tracing → logging
//
// The circuit breaker wraps retry so an open breaker fails fast instead of being
// retried into (R2). The timeout sits inside retry so it bounds each attempt
// (a per-attempt child context) while the retrier's MaxElapsedTime bounds the
// whole logical call (R8). Metrics is outermost so it records one row per logical
// operation, not one per retry attempt (R10).
func (b *ClientBuilder) chain() []connect.Interceptor {
	var chain []connect.Interceptor
	if b.metrics != nil {
		chain = append(chain, MetricsInterceptor(b.metrics))
	}
	if b.cb != nil {
		chain = append(chain, CircuitBreakerInterceptor(b.cb))
	}
	if b.retrier != nil {
		chain = append(chain, RetryInterceptor(b.retrier))
	}
	if b.timeout > 0 {
		chain = append(chain, TimeoutInterceptor(b.timeout))
	}
	if b.tracer != nil {
		chain = append(chain, ClientTracingInterceptor(b.tracer))
	}
	if b.logger != nil {
		chain = append(chain, NewLoggingInterceptor(b.logger))
	}
	return chain
}

// Build assembles the interceptor chain in canonical client order and returns
// client options ready for use with generated Connect client constructors.
// Returns nil if no interceptors are configured.
func (b *ClientBuilder) Build() []connect.ClientOption {
	chain := b.chain()
	if len(chain) == 0 {
		return nil
	}
	return []connect.ClientOption{connect.WithInterceptors(chain...)}
}
