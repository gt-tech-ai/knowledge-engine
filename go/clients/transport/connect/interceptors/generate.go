package interceptors

// Mocks for the third-party transport seams the connect interceptor tests drive:
// the stdlib http.RoundTripper (a must-not-be-hit transport for the fail-fast
// breaker test) and connect's StreamingHandlerConn (the streaming auth path). Both
// live behind connectrpc/net-http deps this module already carries, so they are
// generated here rather than from the dependency-free core module.
//go:generate mockgen -destination=../../../../tests/mocks/mock_round_tripper.go -package=mocks net/http RoundTripper
//go:generate mockgen -destination=../../../../tests/mocks/mock_stream_conn.go -package=mocks connectrpc.com/connect StreamingHandlerConn
