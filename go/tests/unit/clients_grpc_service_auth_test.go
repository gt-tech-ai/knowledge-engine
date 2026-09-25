package unit_test

import (
	"context"
	"testing"

	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine/go/clients/rpc/grpc/interceptors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestServiceAuthClientInterceptor_AttachesBearer tests that the gRPC client interceptor
// attaches the caller's service-to-service token as `authorization: Bearer <token>`
// outgoing metadata — the client half of for the api→identity hop — and attaches
// nothing when no token is configured (dev bypass).
//
// Why this test is important:
//   - The server-side WithServiceAuth rejects an unauthenticated caller; the api→identity
//     client must SEND its token or every Resolve would fail-closed once enforcement is on
//   - An empty token must attach no header so local dev (server stub) keeps working
//
// What it tests:
//   - a non-empty token → outgoing metadata carries "Bearer <token>"; empty → no metadata
func TestServiceAuthClientInterceptor_AttachesBearer(t *testing.T) {
	t.Parallel()

	capture := func() (grpc.UnaryInvoker, *string) {
		var got string
		inv := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			if md, ok := metadata.FromOutgoingContext(ctx); ok {
				if vals := md.Get("authorization"); len(vals) > 0 {
					got = vals[0]
				}
			}
			return nil
		}
		return inv, &got
	}

	t.Run("attaches bearer for a configured token", func(t *testing.T) {
		t.Parallel()
		inv, got := capture()
		err := grpcinterceptors.ServiceAuthClientInterceptor("tok-api")(
			context.Background(),
			"/internalapi.v1.IdentityService/Resolve",
			nil,
			nil,
			nil,
			inv,
		)
		assert.NoError(t, err)
		assert.Equal(t, "Bearer tok-api", *got)
	})

	t.Run("attaches nothing for an empty token", func(t *testing.T) {
		t.Parallel()
		inv, got := capture()
		err := grpcinterceptors.ServiceAuthClientInterceptor("")(
			context.Background(),
			"/internalapi.v1.IdentityService/Resolve",
			nil,
			nil,
			nil,
			inv,
		)
		assert.NoError(t, err)
		assert.Empty(t, *got, "an empty token must attach no authorization metadata")
	})
}

// errCaptured stops a captured call after its outgoing metadata is recorded.
var errCaptured = status.Error(codes.Aborted, "captured")

// bearerOf returns the outgoing `authorization` metadata value on ctx ("" if none).
func bearerOf(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	if vals := md.Get("authorization"); len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// TestGRPCStreamingClientBuilder_WithServiceAuth_AuthenticatesEveryRPC tests that a
// streaming client built with a service token presents it on every RPC of the
// connection — stream opens and unary calls alike.
//
// Why this test is important:
//   - A streaming client (e.g. a query stream plus a unary side call on one conn)
//     calling an internal server that enforces service auth is rejected on any RPC
//     that lacks the bearer token, so the token must ride both stream and unary calls.
//   - An empty token must attach nothing so a local-dev server bypass keeps working.
//
// What it tests:
//   - With WithServiceAuth("tok-api"), a stream open and a unary call on a conn built
//     from Build()'s options both carry "Bearer tok-api"; with an empty token Build
//     returns no options and nothing is attached.
func TestGRPCStreamingClientBuilder_WithServiceAuth_AuthenticatesEveryRPC(t *testing.T) {
	t.Parallel()

	var streamBearer, unaryBearer string
	captureStream := func(
		ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string,
		_ grpc.Streamer, _ ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		streamBearer = bearerOf(ctx)
		return nil, errCaptured
	}
	captureUnary := func(
		ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn,
		_ grpc.UnaryInvoker, _ ...grpc.CallOption,
	) error {
		unaryBearer = bearerOf(ctx)
		return errCaptured
	}

	opts := grpcinterceptors.NewStreamingClientBuilder().WithServiceAuth("tok-api").Build()
	conn, err := grpc.NewClient(
		"passthrough:///streaming-service-auth-test",
		append(opts,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithChainStreamInterceptor(captureStream),
			grpc.WithChainUnaryInterceptor(captureUnary),
		)...,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = conn.NewStream(context.Background(), &grpc.StreamDesc{}, "/test.Service/Stream")
	require.ErrorIs(t, err, errCaptured)
	require.ErrorIs(t, conn.Invoke(context.Background(), "/test.Service/Unary", nil, nil), errCaptured)

	assert.Equal(t, "Bearer tok-api", streamBearer, "the stream open carries the token")
	assert.Equal(t, "Bearer tok-api", unaryBearer, "the unary call carries the token")
	assert.Nil(t, grpcinterceptors.NewStreamingClientBuilder().WithServiceAuth("").Build())
}
