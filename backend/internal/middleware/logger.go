package middleware

import (
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// healthPath is not logged, to keep probes out of the access log.
const healthPath = "/healthz"

// Logger logs one structured line per request: method, path, status, bytes,
// duration, request ID and remote IP. Requests to /healthz are skipped. 5xx
// responses log at error level, 4xx at warn, everything else at info.
// Mount it after RequestID so the ID is available.
func Logger(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == healthPath {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rw := newResponseWriter(w)
			next.ServeHTTP(rw, r)

			var ev *zerolog.Event
			switch {
			case rw.status >= 500:
				ev = log.Error()
			case rw.status >= 400:
				ev = log.Warn()
			default:
				ev = log.Info()
			}
			ev.Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", rw.status).
				Int64("bytes", rw.bytes).
				Dur("duration", time.Since(start)).
				Str("request_id", RequestIDFromContext(r.Context())).
				Str("remote_ip", clientIP(r)).
				Msg("http request")
		})
	}
}
