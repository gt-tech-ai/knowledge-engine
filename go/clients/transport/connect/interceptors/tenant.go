package interceptors

import (
	"context"

	"connectrpc.com/connect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	coretenant "github.com/gt-tech-ai/knowledge-engine/go/core/tenant"
)

// TenantExtractor returns the request's resolved tenant id, or false when none is
// resolved (a public or unauthenticated request).
type TenantExtractor func(ctx context.Context) (uuid.UUID, bool)

// TenantStamper returns ctx scoped to tenant for one persistence mechanism — e.g.
// a Postgres session variable for Row-Level Security, or an ORM tenant scope.
type TenantStamper func(ctx context.Context, tenant uuid.UUID) context.Context

// StampTenantContext stamps the tenant with core/tenant.WithTenant, the context
// value data-access layers read to scope operations and fail closed without one.
func StampTenantContext(ctx context.Context, tenant uuid.UUID) context.Context {
	return coretenant.WithTenant(ctx, tenant)
}

// SessionVarStamper returns a stamper that sets the Postgres session variable
// (GUC) name to the tenant id through Ent's sql.WithVar: Ent emits SET before each
// statement, which drives a Row-Level Security policy reading
// current_setting(name, true) (leak-safe on reads: Ent RESETs the pooled conn).
func SessionVarStamper(name string) TenantStamper {
	return func(ctx context.Context, tenant uuid.UUID) context.Context {
		return entsql.WithVar(ctx, name, tenant.String())
	}
}

// tenantScopeInterceptor stamps the extracted tenant onto the request context with
// each stamper, for BOTH unary and streaming RPCs.
type tenantScopeInterceptor struct {
	// extract resolves the request's tenant.
	extract TenantExtractor
	// stampers apply the tenant scope, in order.
	stampers []TenantStamper
}

// NewTenantScopeInterceptor returns an interceptor that stamps the tenant resolved
// by extract onto each request with every stamper. It must run after whatever
// resolves the tenant (typically the principal interceptor). A request with no
// resolved tenant passes through unstamped, so tenant-scoped data access downstream
// fails closed while non-tenant access is unaffected.
func NewTenantScopeInterceptor(
	extract TenantExtractor,
	stampers ...TenantStamper,
) connect.Interceptor {
	return tenantScopeInterceptor{extract: extract, stampers: stampers}
}

// WrapUnary stamps the tenant on the unary request context.
func (i tenantScopeInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(i.stamp(ctx), req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side propagation interceptor.
func (i tenantScopeInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler stamps the tenant on the streaming request context, so a
// server-streaming handler carries the same tenant scope.
func (i tenantScopeInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(i.stamp(ctx), conn)
	}
}

// stamp applies every stamper for the extracted tenant, or returns ctx unchanged
// when none is resolved.
func (i tenantScopeInterceptor) stamp(ctx context.Context) context.Context {
	tenant, ok := i.extract(ctx)
	if !ok {
		return ctx
	}
	for _, s := range i.stampers {
		ctx = s(ctx, tenant)
	}
	return ctx
}
