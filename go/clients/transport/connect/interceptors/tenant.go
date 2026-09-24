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

// NewTenantInterceptor returns the tenant-propagation interceptor: a tenant-scope
// interceptor that takes the tenant from the identity interceptor's
// UserPermissions.OrgID and stamps two values:
//   - the TenantSessionVar Postgres session variable (SessionVarStamper), which
//     drives Row-Level Security;
//   - the core/tenant context (StampTenantContext), read by the Ent tenant
//     interceptor + hook for app-layer, fail-closed scoping.
//
// It MUST run AFTER the identity interceptor (so OrgID is resolved) and before
// request handling. A request with no resolved permissions or no org passes through
// unstamped.
func NewTenantInterceptor() connect.Interceptor {
	return NewTenantScopeInterceptor(permsTenant, permsStampers...)
}

// permsStampers are the two stamps NewTenantInterceptor and WithTenantFromPerms apply.
var permsStampers = []TenantStamper{SessionVarStamper(TenantSessionVar), StampTenantContext}

// permsTenant extracts the tenant from the context's UserPermissions: its OrgID, or
// false when there are no permissions or no org.
func permsTenant(ctx context.Context) (uuid.UUID, bool) {
	perms, ok := GetUserPermissions(ctx)
	if !ok || perms == nil || perms.OrgID == uuid.Nil {
		return uuid.Nil, false
	}
	return perms.OrgID, true
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
	for _, s := range permsStampers {
		ctx = s(ctx, perms.OrgID)
	}
	return ctx
}
