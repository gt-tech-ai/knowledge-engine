// Package rpc provides Connect RPC utilities for bidirectional mapping between
// domain AppErrors and Connect/gRPC status codes.
package rpc

import (
	"context"

	"connectrpc.com/connect"
	"github.com/gt-tech-ai/knowledge-engine/go/core/errctx"
	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Sanitize maps a domain AppError to a client-safe Connect code and a generic
// message, stripping any implementation detail (stack traces, SQL, upstream
// provider text) that must never reach an API consumer.
//
// It performs the mapping only — it does not touch the request context or build
// a connect.Error. ToConnectError uses it for the RPC's top-level error, while
// handlers that must embed a sanitized failure in a response body (e.g. the
// per-invite failures of a batch invite) call it directly and remain
// responsible for recording the real cause server-side.
func Sanitize(err error) (code connect.Code, message string) {
	switch apperr.Code(err) {
	case apperr.CodeNotFound:
		return connect.CodeNotFound, "resource not found"
	case apperr.CodeInvalidInput:
		return connect.CodeInvalidArgument, "invalid input"
	case apperr.CodeConflict:
		return connect.CodeAlreadyExists, "resource already exists"
	case apperr.CodeUnauthorized:
		return connect.CodeUnauthenticated, "unauthenticated"
	case apperr.CodeForbidden:
		return connect.CodePermissionDenied, "permission denied"
	case apperr.CodeUpstream:
		return connect.CodeUnavailable, "upstream service unavailable"
	case apperr.CodeCanceled:
		return connect.CodeCanceled, "request canceled"
	case apperr.CodeTimeout:
		return connect.CodeDeadlineExceeded, "deadline exceeded"
	default:
		return connect.CodeInternal, "internal server error"
	}
}

// ToConnectError maps a domain AppError to a Connect error code and returns a
// client-safe error.
//
// The real error is captured into the request's error sink (errctx) so the
// request-logging interceptor can record the true cause once, while the value
// returned to the client carries only a sanitized message. This prevents
// leaking stack traces, SQL errors, or other implementation details to API
// consumers.
func ToConnectError(ctx context.Context, err error) error {
	// Record the real error for the request logger before sanitizing it.
	errctx.Capture(ctx, err)

	code, msg := Sanitize(err)
	return connect.NewError(code, apperr.Sentinel(msg))
}

// FromRPCError maps a Connect or gRPC status error to a domain AppError.
// Nil in returns nil. Errors that are already AppErrors are returned unchanged.
//
// Connect codes are preferred when present; otherwise a gRPC status is used
// (Connect's CodeOf does not unwrap status.Error). Unrecognized wire codes and
// plain errors map to CodeUnknown.
func FromRPCError(err error) error {
	if err == nil {
		return nil
	}
	if apperr.Code(err) != apperr.CodeUnknown {
		return err
	}

	var code apperr.ErrorCode
	var msg string
	wire := connect.CodeOf(err)
	if wire == connect.CodeUnknown {
		if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
			wire = connect.Code(st.Code())
		}
	}

	switch wire {
	case connect.CodeNotFound:
		code, msg = apperr.CodeNotFound, "resource not found"
	case connect.CodeInvalidArgument:
		code, msg = apperr.CodeInvalidInput, "invalid input"
	case connect.CodeAlreadyExists:
		code, msg = apperr.CodeConflict, "resource already exists"
	case connect.CodeUnauthenticated:
		code, msg = apperr.CodeUnauthorized, "unauthenticated"
	case connect.CodePermissionDenied:
		code, msg = apperr.CodeForbidden, "permission denied"
	case connect.CodeUnavailable:
		code, msg = apperr.CodeUpstream, "upstream service unavailable"
	case connect.CodeDeadlineExceeded:
		code, msg = apperr.CodeTimeout, "deadline exceeded"
	case connect.CodeCanceled:
		code, msg = apperr.CodeCanceled, "request canceled"
	case connect.CodeInternal:
		code, msg = apperr.CodeInternal, "internal server error"
	default:
		code, msg = apperr.CodeUnknown, "unknown error"
	}
	return apperr.Wrap(err, code, msg)
}
