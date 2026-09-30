// Package middleware provides chi-compatible HTTP middleware
// (func(http.Handler) http.Handler) for the CallGo.mn API: request IDs,
// structured access logging, panic recovery, rate limiting, body limits,
// security headers and request timeouts.
package middleware

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
)

// errorBody is the API error envelope documented in docs/API.md.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes the standard JSON error envelope.
func writeError(w http.ResponseWriter, status int, code, message string) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Del("Content-Length")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{Code: code, Message: message}})
}

// responseWriter records the status code and body size while still
// supporting http.Hijacker (WebSocket upgrades) and http.Flusher.
type responseWriter struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *responseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	// 1xx informational responses do not finalise the header.
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.wroteHeader = true
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher.
func (w *responseWriter) Flush() {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker.
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("middleware: underlying ResponseWriter does not support hijacking")
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		w.wroteHeader = true
		w.status = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// clientIP returns the host part of r.RemoteAddr. Mount chi's RealIP (or an
// equivalent) before these middleware when running behind a trusted proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
