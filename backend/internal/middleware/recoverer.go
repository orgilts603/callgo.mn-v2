package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/rs/zerolog"
)

// Recoverer converts handler panics into a 500 JSON error and logs the panic
// with its stack trace. http.ErrAbortHandler is re-panicked as net/http
// expects. If the response has already started, no body is written.
func Recoverer(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := newResponseWriter(w)
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel value compared by net/http itself
					panic(rec)
				}
				log.Error().
					Interface("panic", rec).
					Bytes("stack", debug.Stack()).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("request_id", RequestIDFromContext(r.Context())).
					Msg("panic recovered")
				if !rw.wroteHeader {
					writeError(rw, http.StatusInternalServerError, "internal", "internal error")
				}
			}()
			next.ServeHTTP(rw, r)
		})
	}
}
