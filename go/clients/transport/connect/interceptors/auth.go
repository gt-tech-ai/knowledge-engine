// Package interceptors provides Connect RPC interceptors for cross-cutting concerns.
//
// Auth interceptor extracts identity headers (set by Kong after identity service
// validation) and stores them as AuthClaims in the request context. Downstream
// handlers retrieve claims via GetAuthClaims.
package interceptors

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
)

const (
	// HeaderAuthSub carries the authenticated user's external subject ID (auth0 ID).
	HeaderAuthSub = "X-User-Sub"

	// HeaderAuthOrgID carries the organization the user is authenticating under.
	HeaderAuthOrgID = "X-Org-ID"

	// HeaderAuthEmail carries the authenticated user's email address.
	HeaderAuthEmail = "X-User-Email"

	// HeaderAuthName carries the authenticated user's full name.
	HeaderAuthName = "X-User-Name"

	// HeaderAuthNickName carries the authenticated user's display name.
	HeaderAuthNickName = "X-User-Nickname"

	// HeaderAuthRoles carries the user's comma-separated role list.
	HeaderAuthRoles = "X-Roles"
)

// HeaderMap names the gateway headers the auth interceptor reads claims from, so a
// consumer matches whatever its gateway sets.
type HeaderMap struct {
	// Sub carries the authenticated caller's external subject id; its absence means
	// the request is unauthenticated.
	Sub string
	// Tenant carries the tenant (organization) the caller authenticates under.
	Tenant string
	// Email carries the caller's email address.
	Email string
	// Name carries the caller's full name.
	Name string
	// NickName carries the caller's display name.
	NickName string
	// Roles carries the caller's comma-separated role list.
	Roles string
}

// DefaultHeaderMap returns the header names NewAuthInterceptor reads (the
// HeaderAuth* constants).
func DefaultHeaderMap() HeaderMap {
	return HeaderMap{
		Sub:      HeaderAuthSub,
		Tenant:   HeaderAuthOrgID,
		Email:    HeaderAuthEmail,
		Name:     HeaderAuthName,
		NickName: HeaderAuthNickName,
		Roles:    HeaderAuthRoles,
	}
}

// authContextKey is the private context key under which AuthClaims are stored,
// keeping the claims slot collision-free across packages.
type authContextKey struct{}

// AuthClaims holds authentication claims extracted from identity headers.
// NOTE: These headers will be set by Kong after validating the JWT.
type AuthClaims struct {
	// Sub is the authenticated user's unique identifier external ID (auth0 ID).
	Sub string

	// Email is the authenticated user's email address.
	Email string

	// Name is the authenticated user's full name (e.g. "Jane Smith")
	//
	// On initial signup, auth0 often sets this to be the user's email address.
	Name string

	// NickName is the authenticated user's display name (e.g. "jane.smith").
	NickName string

	// OrgID is the organization the user is authenticating under.
	OrgID string

	// InitialRoles is the caller's Auth0 role list carried in the token, used ONLY
	// to derive the member's role + clearance when the user is first provisioned in
	// the identity database. It is NOT the source of truth for per-request
	// authorization.
	InitialRoles []string

	// Teams is the caller's team memberships within the org, resolved server-side.
	Teams []TeamMembership

	// Workspaces is the caller's accessible workspaces (via team grants), resolved
	// server-side.
	Workspaces []WorkspaceAccess

	// Synthetic marks claims that were fabricated by the local-dev stub path
	// (no gateway identity present), as opposed to real claims extracted from
	// Kong-injected headers. The identity interceptor uses it to decide whether to
	// short-circuit to the stub org: stub-org injection must apply ONLY to synthetic
	// claims, so that with stub enabled behind Kong a real caller still resolves
	// their true org instead of being forced into the stub org.
	Synthetic bool
}

// WithAuthClaims stores auth claims in the context.
func WithAuthClaims(
	ctx context.Context,
	claims *AuthClaims,
) context.Context {
	return context.WithValue(ctx, authContextKey{}, claims)
}

// GetAuthClaims retrieves auth claims from the context.
func GetAuthClaims(ctx context.Context) (*AuthClaims, bool) {
	claims, ok := ctx.Value(authContextKey{}).(*AuthClaims)
	return claims, ok
}

// newStubDevClaims returns fresh synthetic claims for local dev (auth.stub=true)
// when Kong is absent. A new value per request prevents concurrent handlers from
// sharing the same pointer.
func newStubDevClaims() *AuthClaims {
	return &AuthClaims{
		Sub:          "stub-user",
		Email:        "stub@example.com",
		Name:         "Stub User",
		NickName:     "stub",
		OrgID:        "stub-org",
		InitialRoles: []string{"admin"},
		Synthetic:    true,
	}
}

// authInterceptor extracts identity headers into the request context for BOTH
// unary and streaming RPCs. Streaming coverage is required: Connect applies a
// connect.UnaryInterceptorFunc only to unary calls, so a unary-only auth
// interceptor would leave server-streaming handlers (e.g. QueryStream)
// unauthenticated.
type authInterceptor struct {
	// headers names the gateway headers claims are read from.
	headers HeaderMap
	// stub injects synthetic dev claims when Kong is absent (local dev only).
	stub bool
}

// NewAuthInterceptor returns an interceptor that extracts identity headers into
// context for unary and streaming handlers.
// Headers consumed: X-User-Sub, X-Org-ID, X-User-Email, X-User-Name, X-User-Nickname,
// X-Roles (comma-separated). Clearance is NOT read from a header — it is DB-resolved from
// the caller's membership (see identity resolution); the X-Clearance-Level header is
// no longer consumed.
// Token validation is NOT performed here -- it is delegated to Kong.
//
// When stub is true (auth.stub: true in dev config), synthetic dev claims are injected
// when no X-User-Sub header is present so local development works without Kong/Auth0.
// When stub is false (staging/prod), a request with no X-User-Sub is left unauthenticated;
// protected handlers must call GetAuthClaims and return codes.Unauthenticated.
func NewAuthInterceptor(stub bool) connect.Interceptor {
	return NewAuthInterceptorWithHeaders(stub, DefaultHeaderMap())
}

// NewAuthInterceptorWithHeaders is NewAuthInterceptor reading claims from the
// headers named in headers instead of the defaults.
func NewAuthInterceptorWithHeaders(stub bool, headers HeaderMap) connect.Interceptor {
	return authInterceptor{headers: headers, stub: stub}
}

// WrapUnary extracts identity headers from the unary request.
func (a authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(a.withClaims(ctx, req.Header()), req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side header-extraction interceptor.
func (a authInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler extracts identity headers from the streaming request so
// streaming handlers see the same AuthClaims as unary handlers.
func (a authInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(a.withClaims(ctx, conn.RequestHeader()), conn)
	}
}

// withClaims parses the identity headers into AuthClaims and stores them on the
// returned context. With no subject header it either injects stub dev claims (stub
// mode) or leaves the context unauthenticated.
func (a authInterceptor) withClaims(ctx context.Context, h http.Header) context.Context {
	sub := h.Get(a.headers.Sub)

	if sub == "" && a.stub {
		// Dev shortcut: Kong is absent, synthesize canonical dev claims.
		return WithAuthClaims(ctx, newStubDevClaims())
	}
	if sub == "" {
		return ctx
	}

	claims := &AuthClaims{
		Sub:      sub,
		Email:    h.Get(a.headers.Email),
		Name:     h.Get(a.headers.Name),
		NickName: h.Get(a.headers.NickName),
		OrgID:    h.Get(a.headers.Tenant),
	}
	if roles := h.Get(a.headers.Roles); roles != "" {
		claims.InitialRoles = splitRoles(roles)
	}
	return WithAuthClaims(ctx, claims)
}

// splitRoles splits a comma-separated roles string into a slice.
// Empty segments are discarded.
func splitRoles(s string) []string {
	var roles []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			role := s[start:i]
			if role != "" {
				roles = append(roles, role)
			}
			start = i + 1
		}
	}
	return roles
}
