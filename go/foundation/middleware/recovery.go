package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gt-tech-ai/knowledge-engine/go/core/interfaces"
)

// Recovery returns middleware that catches panics and returns 500 Internal Server Error.
func Recovery(logger interfaces.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					stack := debug.Stack()
					logger.WithContext(r.Context()).Error(
						"panic recovered",
						"panic", rec,
						"stack", string(stack),
						"method", r.Method,
						"path", r.URL.Path,
					)
					http.Error(
						w,
						http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError,
					)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
