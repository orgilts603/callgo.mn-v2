package middleware

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// MaxBody limits request bodies to n bytes. Requests declaring a larger
// Content-Length are rejected up front with 413; for others, reads beyond the
// limit fail with *http.MaxBytesError.
func MaxBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				writeError(w, http.StatusRequestEntityTooLarge, "invalid",
					"request body exceeds "+strconv.FormatInt(n, 10)+" bytes")
				return
			}
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SecureHeaders sets conservative security headers on every response.
// Strict-Transport-Security is only sent for requests that arrived over TLS
// (directly or via X-Forwarded-Proto).
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// Timeout bounds request processing: the request context gets a deadline d,
// which context-aware handlers (database, HTTP clients) honour. If the handler
// returns after the deadline without having written a response, a 503 JSON
// error is sent. Unlike http.TimeoutHandler it does not buffer the response, so
// WebSocket upgrades and streaming keep working; mount it only on routes that
// are expected to finish within d.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			rw := newResponseWriter(w)
			next.ServeHTTP(rw, r.WithContext(ctx))
			if !rw.wroteHeader && ctx.Err() == context.DeadlineExceeded {
				writeError(rw, http.StatusServiceUnavailable, "internal", "request timed out")
			}
		})
	}
}
