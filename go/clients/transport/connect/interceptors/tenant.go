package interceptors

import (
	"context"

	"connectrpc.com/connect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	coretenant "github.com/gt-tech-ai/knowledge-engine/go/core/tenant"
)

// TenantSessionVar is the Postgres session variable (GUC) the tenant is propagated
// through. The Row-Level Security policy on the tenant tables reads it via
// current_setting('app.current_tenant', true); keep this in lockstep with the RLS
// migration.
const TenantSessionVar = "app.current_tenant"

// tenantInterceptor propagates the caller's resolved organization (tenant) to the
// persistence layer for BOTH unary and streaming RPCs. It stamps two context values
// from the identity interceptor's UserPermissions.OrgID:
//   - sql.WithVar(app.current_tenant, <org>) — Ent emits SET before each statement,
//     which drives Postgres RLS (leak-safe on reads: Ent RESETs the base-pool conn);
//   - coretenant.WithTenant(<org>) — read by the Ent tenant interceptor + hook for the
//     app-layer, fail-closed scoping.
//
// It MUST run AFTER the identity interceptor (so OrgID is resolved) and before
// request handling. A request with no resolved permissions (public/unauthenticated)
// or no org passes through unstamped: the Ent tenant interceptor then fails closed
// on any tenant-scoped access, and non-tenant access is unaffected.
type tenantInterceptor struct{}

// NewTenantInterceptor returns the tenant-propagation interceptor.
func NewTenantInterceptor() connect.Interceptor {
	return tenantInterceptor{}
}

// WrapUnary stamps the tenant on the unary request context.
func (tenantInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(withTenant(ctx), req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side propagation interceptor.
func (tenantInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler stamps the tenant on the streaming request context, so a
// server-streaming handler (e.g. QueryStream) carries the same tenant scope.
func (tenantInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(withTenant(ctx), conn)
	}
}

// withTenant returns a context stamped with the resolved org's tenant scope, or the
// original context when no org is resolved (pass through — the Ent tenant
// interceptor fails closed on tenant-scoped access downstream).
func withTenant(ctx context.Context) context.Context {
	perms, ok := GetUserPermissions(ctx)
	if !ok {
		return ctx
	}
	return WithTenantFromPerms(ctx, perms)
}

// WithTenantFromPerms stamps perms' org as the tenant scope on ctx — the RLS session var AND
// the Ent tenant context, the same two stamps the tenant interceptor applies. It exists for a
// transport that holds the caller's permissions directly and so bypasses the Connect
// interceptor chain — namely the WebSocket query path, whose per-query context is built at the
// message boundary rather than by a unary/streaming handler. A nil perms or zero org passes
// through unstamped, so the Ent tenant interceptor still fails closed on tenant-scoped access.
func WithTenantFromPerms(ctx context.Context, perms *UserPermissions) context.Context {
	if perms == nil || perms.OrgID == uuid.Nil {
		return ctx
	}
	ctx = entsql.WithVar(ctx, TenantSessionVar, perms.OrgID.String())
	return coretenant.WithTenant(ctx, perms.OrgID)
}
