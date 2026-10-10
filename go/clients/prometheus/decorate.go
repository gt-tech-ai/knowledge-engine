package prometheus

import (
	"bytes"
	"context"
	"io"
	"net/http"

	clientdecorators "github.com/gt-tech-ai/knowledge-engine/go/clients/decorators"
	coreerr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// DecorateDoer runs every request inner sends through the client stack. Each attempt
// reads the whole response body before the stack's per-attempt timeout ends, so the
// returned body is buffered and the deadline cannot cut a read short. Query requests are
// GETs, so every attempt is retryable; a transport error is coded CodeUnavailable unless
// it already carries a code. An HTTP error status is not an error here: the response
// passes through and the client codes it.
func DecorateDoer(inner HTTPDoer, stack *clientdecorators.Stack) HTTPDoer {
	return &decoratedDoer{inner: inner, stack: stack}
}

// decoratedDoer is the HTTPDoer that sends through the client stack.
type decoratedDoer struct {
	// inner sends each attempt.
	inner HTTPDoer

	// stack applies the client-boundary layers.
	stack *clientdecorators.Stack
}

// Do sends req through the stack and returns its response with a buffered body.
func (d *decoratedDoer) Do(req *http.Request) (*http.Response, error) {
	return clientdecorators.Run(req.Context(), d.stack, "prometheus.query",
		clientdecorators.RunOpts{Retryable: true},
		func(ctx context.Context) (*http.Response, error) {
			resp, err := d.inner.Do(req.Clone(ctx))
			if err != nil {
				return nil, coreerr.Wrap(err, coreerr.CodeOr(err, coreerr.CodeUnavailable),
					"prometheus request")
			}
			src := resp.Body
			defer func() { _ = src.Close() }()
			body, err := io.ReadAll(src)
			if err != nil {
				return nil, coreerr.Wrap(err, coreerr.CodeUnavailable, "read prometheus response")
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		})
}
