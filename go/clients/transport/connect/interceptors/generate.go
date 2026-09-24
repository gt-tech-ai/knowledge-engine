package interceptors

// Mock generation for the interceptor's consumer-facing resolver port. The mock
// lives in go/tests/mocks so black-box tests (go/tests/unit) can drive
// the enrichment interceptor against a mocked IdentityResolver.

//go:generate mockgen -destination=../../../../tests/mocks/mock_interceptor_resolver.go -package=mocks github.com/gt-tech-ai/knowledge-engine/go/clients/transport/connect/interceptors IdentityResolver

// Mocks for the third-party transport seams the connect interceptor tests drive:
// the stdlib http.RoundTripper (a must-not-be-hit transport for the fail-fast
// breaker test) and connect's StreamingHandlerConn (the streaming auth path). Both
// live behind connectrpc/net-http deps this module already carries, so they are
// generated here rather than from the dependency-free core module.
//go:generate mockgen -destination=../../../../tests/mocks/mock_round_tripper.go -package=mocks net/http RoundTripper
//go:generate mockgen -destination=../../../../tests/mocks/mock_stream_conn.go -package=mocks connectrpc.com/connect StreamingHandlerConn
