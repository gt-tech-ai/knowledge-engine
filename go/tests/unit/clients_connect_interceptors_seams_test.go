package unit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/gt-tech-ai/knowledge-engine/go/clients/transport/connect/interceptors"
	coretenant "github.com/gt-tech-ai/knowledge-engine/go/core/tenant"
)

// principal is a consumer-owned principal type, standing in for an application's own
// authorization model.
type principal struct {
	Sub    string
	Tenant string
}

// customHeaders is a gateway header contract that differs from testHeaders.
var customHeaders = interceptors.HeaderMap{
	Sub:      "X-Sub",
	Tenant:   "X-Tenant",
	Email:    "X-Mail",
	Name:     "X-Full-Name",
	NickName: "X-Nick",
	Roles:    "X-Groups",
}

// TestAuthInterceptor_HeaderMap tests that the auth interceptor reads claims from the
// headers a consumer's gateway actually sets.
//
// Why this test is important:
//   - Gateways name identity headers differently; a fixed header contract ties the library
//     to one deployment.
//
// What it tests:
//   - With a custom HeaderMap, every claim is read from the custom headers.
//   - Headers outside the map are ignored.
func TestAuthInterceptor_HeaderMap(t *testing.T) {
	t.Parallel()
	ic := interceptors.NewAuthInterceptor(false, customHeaders)

	ctx, err := invokeUnaryWith(ic, newTestRequestWithHeaders(map[string]string{
		"X-Sub": "u1", "X-Tenant": "t1", "X-Mail": "u1@example.com",
		"X-Full-Name": "User One", "X-Nick": "u1", "X-Groups": "a,b",
	}))
	require.NoError(t, err)
	claims, ok := interceptors.GetAuthClaims(ctx)
	require.True(t, ok, "claims must be read from the custom headers")
	assert.Equal(t, interceptors.AuthClaims{
		Sub: "u1", TenantID: "t1", Email: "u1@example.com", Name: "User One", NickName: "u1",
		Roles: []string{"a", "b"},
	}, *claims)

	ctx, err = invokeUnaryWith(ic, newTestRequestWithHeaders(map[string]string{
		testHeaders.Sub: "u1",
	}))
	require.NoError(t, err)
	_, ok = interceptors.GetAuthClaims(ctx)
	assert.False(t, ok, "a header outside the map must be ignored")
}

// TestPrincipalInterceptor_ResolvesConsumerPrincipal tests that the principal interceptor
// turns claims into the consumer's own principal type.
//
// Why this test is important:
//   - Authorization models are application-specific; the library must carry the
//     consumer's principal, not define one.
//
// What it tests:
//   - Claims are resolved into the consumer's principal, readable via PrincipalFrom.
//   - The raw claims are cleared once resolved.
//   - A request without claims passes through with no principal and no resolver call.
func TestPrincipalInterceptor_ResolvesConsumerPrincipal(t *testing.T) {
	t.Parallel()
	calls := 0
	resolve := func(_ context.Context, c *interceptors.AuthClaims) (principal, error) {
		calls++
		return principal{Sub: c.Sub, Tenant: c.TenantID}, nil
	}
	ic := interceptors.NewPrincipalInterceptor[principal](resolve, nil)

	ctx := interceptors.WithAuthClaims(context.Background(),
		&interceptors.AuthClaims{Sub: "u1", TenantID: "t1"})
	got, err := invokeUnary(ic, ctx)
	require.NoError(t, err)
	p, ok := interceptors.PrincipalFrom[principal](got)
	require.True(t, ok)
	assert.Equal(t, principal{Sub: "u1", Tenant: "t1"}, p)
	claims, _ := interceptors.GetAuthClaims(got)
	assert.Nil(t, claims, "raw claims must be cleared once resolved")

	got, err = invokeUnary(ic, context.Background())
	require.NoError(t, err)
	_, ok = interceptors.PrincipalFrom[principal](got)
	assert.False(t, ok, "no claims → no principal")
	assert.Equal(t, 1, calls, "no claims → no resolver call")
}

// TestPrincipalInterceptor_StubAndErrors tests the local-dev stub path and how resolver
// failures reach the caller.
//
// Why this test is important:
//   - The stub must apply only to synthetic claims, or a real caller behind the gateway is
//     silently given the stub identity; a transient resolver outage must stay retryable
//     rather than logging the caller out.
//
// What it tests:
//   - Synthetic claims get the stub principal without a resolver call; real claims resolve.
//   - A resolver error keeps its Connect code; an uncoded error becomes Unavailable.
func TestPrincipalInterceptor_StubAndErrors(t *testing.T) {
	t.Parallel()
	stub := func(c *interceptors.AuthClaims) principal { return principal{Sub: c.Sub, Tenant: "stub"} }
	resolve := func(_ context.Context, c *interceptors.AuthClaims) (principal, error) {
		return principal{Sub: c.Sub, Tenant: "resolved"}, nil
	}
	ic := interceptors.NewPrincipalInterceptor[principal](resolve, stub)

	got, err := invokeUnary(ic, interceptors.WithAuthClaims(context.Background(),
		&interceptors.AuthClaims{Sub: "dev", Synthetic: true}))
	require.NoError(t, err)
	p, _ := interceptors.PrincipalFrom[principal](got)
	assert.Equal(t, "stub", p.Tenant, "synthetic claims take the stub principal")

	got, err = invokeUnary(ic, interceptors.WithAuthClaims(context.Background(),
		&interceptors.AuthClaims{Sub: "real"}))
	require.NoError(t, err)
	p, _ = interceptors.PrincipalFrom[principal](got)
	assert.Equal(t, "resolved", p.Tenant, "real claims resolve even with a stub set")

	for name, tc := range map[string]struct {
		err  error
		want connect.Code
	}{
		"coded":   {connect.NewError(connect.CodeUnauthenticated, errors.New("unknown user")), connect.CodeUnauthenticated},
		"uncoded": {errors.New("dial"), connect.CodeUnavailable},
	} {
		failing := func(context.Context, *interceptors.AuthClaims) (principal, error) {
			return principal{}, tc.err
		}
		_, err := invokeUnary(interceptors.NewPrincipalInterceptor[principal](failing, nil),
			interceptors.WithAuthClaims(context.Background(), &interceptors.AuthClaims{Sub: "u"}))
		assert.Equal(t, tc.want, connect.CodeOf(err), name)
	}
}

// TestTenantScopeInterceptor_StampsExtractedTenant tests that the tenant interceptor stamps
// whatever tenant the consumer's extractor resolves, with the consumer's stampers.
//
// Why this test is important:
//   - Where the tenant comes from and how it scopes persistence (which session variable,
//     which ORM scope) are application choices; a tenant stamped wrongly leaks data across
//     tenants.
//
// What it tests:
//   - A resolved tenant is stamped by each stamper: the core tenant context and a named
//     SQL session variable.
//   - An unresolved tenant leaves the context unstamped.
func TestTenantScopeInterceptor_StampsExtractedTenant(t *testing.T) {
	t.Parallel()
	tenant := uuid.New()
	resolved := true
	extract := func(context.Context) (uuid.UUID, bool) { return tenant, resolved }
	ic := interceptors.NewTenantScopeInterceptor(extract,
		interceptors.StampTenantContext, interceptors.SessionVarStamper("app.tenant"))

	got, err := invokeUnary(ic, context.Background())
	require.NoError(t, err)
	id, ok := coretenant.TenantFromContext(got)
	assert.True(t, ok)
	assert.Equal(t, tenant, id)
	v, ok := entsql.VarFromContext(got, "app.tenant")
	assert.True(t, ok)
	assert.Equal(t, tenant.String(), v)

	resolved = false
	got, err = invokeUnary(ic, context.Background())
	require.NoError(t, err)
	_, ok = coretenant.TenantFromContext(got)
	assert.False(t, ok, "an unresolved tenant must not be stamped")
}

// TestServerBuilder_CustomHeadersAndCallerInterceptors tests that the server chain uses the
// consumer's header contract and runs the consumer's own interceptors after auth.
//
// Why this test is important:
//   - A consumer that brings its own identity or tenant interceptors needs them to see the
//     authenticated claims; running before auth would authorize an anonymous request.
//
// What it tests:
//   - Over a real Connect round trip, a caller interceptor added with WithInterceptors sees
//     the claims the auth interceptor parsed from the consumer's header contract.
func TestServerBuilder_CustomHeadersAndCallerInterceptors(t *testing.T) {
	t.Parallel()
	var seen *interceptors.AuthClaims
	probe := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			seen, _ = interceptors.GetAuthClaims(ctx)
			return next(ctx, req)
		}
	})
	opts := interceptors.NewServerBuilder().
		WithAuth(false, customHeaders).
		WithInterceptors(probe).
		Build()

	const procedure = "/test.v1.Probe/Do"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return connect.NewResponse(&emptypb.Empty{}), nil
		}, opts...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req := connect.NewRequest(&emptypb.Empty{})
	req.Header().Set("X-Sub", "u1")
	req.Header().Set("X-Tenant", "t1")
	_, err := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure).
		CallUnary(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, seen, "the caller interceptor must run after auth")
	assert.Equal(t, "u1", seen.Sub)
	assert.Equal(t, "t1", seen.TenantID)
}

// invokeUnary runs ic's unary path against a pass-through handler and returns the context the
// handler observed plus the error.
func invokeUnary(
	ic connect.Interceptor, ctx context.Context,
) (context.Context, error) {
	var captured context.Context
	next := func(c context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		captured = c
		return newTestResponse(), nil
	}
	_, err := ic.WrapUnary(next)(ctx, newTestRequest())
	return captured, err
}

// invokeUnaryWith runs ic's unary path on req and returns the context the handler observed.
func invokeUnaryWith(ic connect.Interceptor, req connect.AnyRequest) (context.Context, error) {
	var captured context.Context
	next := func(c context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		captured = c
		return newTestResponse(), nil
	}
	_, err := ic.WrapUnary(next)(context.Background(), req)
	return captured, err
}
