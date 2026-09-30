package middleware

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ok(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func TestRequestID(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	t.Run("generates uuid", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		id := rec.Header().Get(RequestIDHeader)
		_, err := uuid.Parse(id)
		require.NoError(t, err)
		assert.Equal(t, id, seen)
	})

	t.Run("passes through", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(RequestIDHeader, "abc-123")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, "abc-123", rec.Header().Get(RequestIDHeader))
		assert.Equal(t, "abc-123", seen)
	})

	t.Run("rejects unsafe ids", func(t *testing.T) {
		for _, bad := range []string{"has space", strings.Repeat("a", 200), "tab\there"} {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(RequestIDHeader, bad)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.NotEqual(t, bad, rec.Header().Get(RequestIDHeader))
		}
	})
}

func TestLogger(t *testing.T) {
	var buf bytes.Buffer
	log := zerolog.New(&buf)
	h := RequestID(Logger(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("hello"))
	})))

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	req.Header.Set(RequestIDHeader, "rid-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.Equal(t, "GET", line["method"])
	assert.Equal(t, "/api/x", line["path"])
	assert.EqualValues(t, 200, line["status"])
	assert.EqualValues(t, 5, line["bytes"])
	assert.Equal(t, "rid-1", line["request_id"])
	assert.Equal(t, "203.0.113.9", line["remote_ip"])
	assert.Equal(t, "info", line["level"])
	assert.Contains(t, line, "duration")

	buf.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/missing", nil))
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	assert.EqualValues(t, 404, line["status"])
	assert.Equal(t, "warn", line["level"])

	buf.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	assert.Empty(t, buf.String(), "/healthz is not logged")
}

// hijackRecorder is a ResponseWriter that supports Hijack.
type hijackRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

func TestLoggerPreservesHijackAndFlush(t *testing.T) {
	log := zerolog.New(io.Discard)
	hr := &hijackRecorder{ResponseRecorder: httptest.NewRecorder()}
	h := Logger(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, isFlusher := w.(http.Flusher)
		assert.True(t, isFlusher)
		hj, isHijacker := w.(http.Hijacker)
		require.True(t, isHijacker)
		conn, _, err := hj.Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	}))
	h.ServeHTTP(hr, httptest.NewRequest(http.MethodGet, "/ws", nil))
	assert.True(t, hr.hijacked)
}

func TestRecoverer(t *testing.T) {
	var buf bytes.Buffer
	log := zerolog.New(&buf)
	h := RequestID(Recoverer(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.JSONEq(t, `{"error":{"code":"internal","message":"internal error"}}`, rec.Body.String())
	assert.Contains(t, buf.String(), "boom")
	assert.Contains(t, buf.String(), "stack")
	assert.Contains(t, buf.String(), "panic recovered")
}

func TestRecovererAfterPartialWrite(t *testing.T) {
	h := Recoverer(zerolog.New(io.Discard))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic("late")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, "partial", rec.Body.String())
}

func TestRecovererRepanicsAbortHandler(t *testing.T) {
	h := Recoverer(zerolog.New(io.Discard))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}

func TestRateLimit(t *testing.T) {
	rl := NewRateLimiter(1, 3)
	defer rl.Stop()
	now := time.Unix(1_700_000_000, 0)
	rl.now = func() time.Time { return now }
	h := rl.Middleware()(http.HandlerFunc(ok))

	do := func(ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip + ":1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusOK, do("1.1.1.1").Code, "burst request %d", i)
	}
	rec := do("1.1.1.1")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
	assert.JSONEq(t, `{"error":{"code":"rate_limited","message":"too many requests"}}`, rec.Body.String())

	assert.Equal(t, http.StatusOK, do("2.2.2.2").Code, "other clients are independent")

	now = now.Add(1100 * time.Millisecond) // refill one token
	assert.Equal(t, http.StatusOK, do("1.1.1.1").Code)
	assert.Equal(t, http.StatusTooManyRequests, do("1.1.1.1").Code)
}

func TestRateLimitCleanup(t *testing.T) {
	rl := NewRateLimiter(10, 5)
	defer rl.Stop()
	now := time.Unix(1_700_000_000, 0)
	rl.now = func() time.Time { return now }

	rl.allow("a")
	rl.allow("b")
	require.Equal(t, 2, rl.size())

	rl.cleanup()
	assert.Equal(t, 2, rl.size(), "recent buckets are kept")

	now = now.Add(rl.idle + time.Second)
	rl.allow("b") // refreshes b
	rl.cleanup()
	assert.Equal(t, 1, rl.size(), "idle bucket a evicted")
}

func TestRateLimitConcurrent(t *testing.T) {
	rl := NewRateLimiter(1000, 1000)
	defer rl.Stop()
	h := rl.Middleware()(http.HandlerFunc(ok))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				h.ServeHTTP(httptest.NewRecorder(), req)
			}
		}()
	}
	wg.Wait()
}

func TestRateLimitStopIsIdempotent(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.Stop()
	rl.Stop()
}

func TestMaxBody(t *testing.T) {
	readAll := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h := MaxBody(10)(readAll)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789")))
	assert.Equal(t, http.StatusNoContent, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789ab")))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"invalid"`)

	// Unknown Content-Length (chunked) is enforced while reading.
	req := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(strings.NewReader(strings.Repeat("x", 50))))
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusNoContent, rec.Code, "bodyless requests pass")
}

func TestSecureHeaders(t *testing.T) {
	h := SecureHeaders(http.HandlerFunc(ok))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
	assert.Empty(t, rec.Header().Get("Strict-Transport-Security"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.NotEmpty(t, rec.Header().Get("Strict-Transport-Security"))
}

func TestTimeout(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			ok(w, r)
		}
	})

	rec := httptest.NewRecorder()
	Timeout(20*time.Millisecond)(slow).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "timed out")

	rec = httptest.NewRecorder()
	Timeout(time.Second)(http.HandlerFunc(ok)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())

	var deadlineSet bool
	Timeout(time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, deadlineSet = r.Context().Deadline()
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	assert.True(t, deadlineSet)
}

func TestResponseWriterStatusOnlyFirstWins(t *testing.T) {
	rw := newResponseWriter(httptest.NewRecorder())
	rw.WriteHeader(http.StatusTeapot)
	rw.WriteHeader(http.StatusOK)
	assert.Equal(t, http.StatusTeapot, rw.status)
}
