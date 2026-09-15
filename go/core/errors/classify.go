package errors

import "net/http"

// IsTransient returns true if the error represents a transient failure
// that may succeed on retry (timeout, unavailable, certain internal errors).
func IsTransient(err error) bool {
	code := Code(err)
	switch code {
	case ErrTimeout, ErrUnavailable:
		return true
	default:
		return false
	}
}

// IsPermanent returns true if the error represents a permanent failure
// that will not succeed on retry (not found, invalid input, unauthorized, forbidden, conflict).
func IsPermanent(err error) bool {
	code := Code(err)
	switch code {
	case ErrNotFound, ErrInvalidInput, ErrUnauthorized, ErrForbidden, ErrConflict:
		return true
	default:
		return false
	}
}

// ToHTTPStatus maps an ErrorCode to the corresponding HTTP status code.
func ToHTTPStatus(code ErrorCode) int {
	switch code {
	case ErrNotFound:
		return http.StatusNotFound
	case ErrInvalidInput:
		return http.StatusBadRequest
	case ErrUnauthorized:
		return http.StatusUnauthorized
	case ErrForbidden:
		return http.StatusForbidden
	case ErrConflict:
		return http.StatusConflict
	case ErrTimeout:
		return http.StatusGatewayTimeout
	case ErrUnavailable:
		return http.StatusServiceUnavailable
	case ErrInternal:
		return http.StatusInternalServerError
	case CodeUpstream:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}
