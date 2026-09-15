package unit_test

import (
	"context"
	"testing"

	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine/go/clients/rpc/grpc/interceptors"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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
