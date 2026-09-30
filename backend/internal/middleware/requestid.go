package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// RequestIDHeader is the header used to receive and echo the request ID.
const RequestIDHeader = "X-Request-ID"

// maxRequestIDLen bounds client-supplied IDs so they cannot bloat logs.
const maxRequestIDLen = 128

type ctxKey int

const requestIDKey ctxKey = iota

// RequestID propagates an incoming X-Request-ID (if sane) or generates a UUID,
// stores it in the request context and echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// RequestIDFromContext returns the request ID set by RequestID, or "".
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range id {
		if c < 0x21 || c > 0x7e { // printable, no whitespace
			return false
		}
	}
	return true
}
