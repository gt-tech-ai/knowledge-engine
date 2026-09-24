package interceptors

import (
	"context"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// PrincipalResolver resolves an authenticated caller's claims into the consumer's
// principal type P — its own authorization model (roles, memberships, grants).
type PrincipalResolver[P any] func(ctx context.Context, claims *AuthClaims) (P, error)

// principalKey is the private context key for a principal of type P; each P gets
// its own key, so principals of different types never collide.
type principalKey[P any] struct{}

// WithPrincipal stores the caller's resolved principal in the context.
func WithPrincipal[P any](ctx context.Context, principal P) context.Context {
	return context.WithValue(ctx, principalKey[P]{}, principal)
}

// PrincipalFrom retrieves the caller's resolved principal of type P from the context.
func PrincipalFrom[P any](ctx context.Context) (P, bool) {
	principal, ok := ctx.Value(principalKey[P]{}).(P)
	return principal, ok
}

// principalInterceptor resolves the request's AuthClaims into a principal of type P
// for BOTH unary and streaming RPCs. It must run after the auth interceptor so the
// claims are present. Streaming coverage matters because a unary-only resolver would
// leave server-streaming handlers without an authorization context.
type principalInterceptor[P any] struct {
	// resolve maps claims to the consumer's principal.
	resolve PrincipalResolver[P]
	// stub, when non-nil, builds the principal for synthetic (local-dev) claims
	// without calling resolve.
	stub func(claims *AuthClaims) P
}

// NewPrincipalInterceptor returns an interceptor that resolves each authenticated
// request's claims into the consumer's principal type P and stores it for
// PrincipalFrom. Per RPC:
//   - no AuthClaims (unauthenticated/public request) → pass through
//   - synthetic claims and a non-nil stub → the stub principal, no resolve call; real
//     claims always resolve, so a caller behind the gateway never gets the stub identity
//   - otherwise → resolve; an error fails the request CLOSED, keeping its Connect code
//     (an uncoded error becomes Unavailable, so a transient outage stays retryable)
//
// The raw AuthClaims are cleared from the context handed downstream, so handlers read
// the resolved principal rather than unresolved claims.
func NewPrincipalInterceptor[P any](
	resolve PrincipalResolver[P],
	stub func(claims *AuthClaims) P,
) connect.Interceptor {
	return principalInterceptor[P]{resolve: resolve, stub: stub}
}

// WrapUnary resolves the unary request's principal, failing closed on a resolver error.
func (i principalInterceptor[P]) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resolvedCtx, err := i.withPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		return next(resolvedCtx, req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side resolution interceptor.
func (i principalInterceptor[P]) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler resolves the streaming request's principal, failing closed on a
// resolver error so a streaming call cannot proceed without an authorization context.
func (i principalInterceptor[P]) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		resolvedCtx, err := i.withPrincipal(ctx)
		if err != nil {
			return err
		}
		return next(resolvedCtx, conn)
	}
}

// withPrincipal resolves the caller's principal and returns a context carrying it,
// with the raw AuthClaims cleared.
func (i principalInterceptor[P]) withPrincipal(ctx context.Context) (context.Context, error) {
	claims, ok := GetAuthClaims(ctx)
	ctx = WithAuthClaims(ctx, nil) // Clear raw claims so none leak downstream unresolved.
	if !ok || claims == nil {
		return ctx, nil
	}
	if i.stub != nil && claims.Synthetic {
		return WithPrincipal(ctx, i.stub(claims)), nil
	}
	principal, err := i.resolve(ctx, claims)
	if err != nil {
		code := connect.CodeOf(err)
		if code == connect.CodeUnknown {
			code = connect.CodeUnavailable
		}
		return ctx, connect.NewError(
			code,
			errors.Wrap(err, errors.CodeInternal, "resolve identity"),
		)
	}
	return WithPrincipal(ctx, principal), nil
}
