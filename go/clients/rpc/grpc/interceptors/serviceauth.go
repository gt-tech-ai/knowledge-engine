package interceptors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ServiceAuthClientInterceptor returns a unary client interceptor that attaches the
// caller's service-to-service bearer token as `authorization: Bearer <token>` outgoing
// metadata (Phase 1), so the internal server's service-auth interceptor can
// authenticate this caller. An empty token attaches nothing — local dev leaves the token
// unset and the server's stub admits the call.
func ServiceAuthClientInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// ServiceAuthStreamClientInterceptor returns a stream client interceptor that attaches the
// caller's service-to-service bearer token as `authorization: Bearer <token>` outgoing
// metadata when a stream opens — the streaming counterpart of ServiceAuthClientInterceptor.
// An empty token attaches nothing.
func ServiceAuthStreamClientInterceptor(token string) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		if token != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
		}
		return streamer(ctx, desc, cc, method, opts...)
	}
}
