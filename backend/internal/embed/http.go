package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maxResponseBytes = 64 << 20
	maxErrorBody     = 300
)

// APIError is a non-2xx answer from an embeddings API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("embed: provider returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("embed: provider returned HTTP %d: %s", e.Status, e.Message)
}

// Temporary reports whether retrying may succeed (rate limit / server error).
func (e *APIError) Temporary() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// retryPolicy retries 429/5xx and transport errors with exponential backoff.
type retryPolicy struct {
	attempts int
	base     time.Duration
	max      time.Duration
}

var defaultRetry = retryPolicy{attempts: 4, base: 500 * time.Millisecond, max: 20 * time.Second}

// httpClient is the transport shared by the HTTP embedders.
type httpClient struct {
	client *http.Client
	secret string // redacted from error messages
	retry  retryPolicy
	dims   atomic.Int64
}

// postJSON sends body to url with headers, retrying transient failures, and
// decodes a 2xx JSON answer into out.
func (c *httpClient) postJSON(ctx context.Context, url string, headers map[string]string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("embed: encode request: %w", err)
	}
	attempts := max(c.retry.attempts, 1)
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.backoff(attempt, lastErr)); err != nil {
				return fmt.Errorf("embed: %w (last error: %v)", err, lastErr)
			}
		}
		lastErr = c.do(ctx, url, headers, payload, out)
		if lastErr == nil {
			return nil
		}
		if !retryable(ctx, lastErr) {
			return lastErr
		}
	}
	return lastErr
}

func (c *httpClient) do(ctx context.Context, url string, headers map[string]string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("embed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return &transportError{err: errors.New(c.redact(err.Error())), cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		apiErr := &APIError{Status: resp.StatusCode, Message: c.redact(errorMessage(raw))}
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs >= 0 {
				return &retryAfterError{APIError: apiErr, after: time.Duration(secs) * time.Second}
			}
		}
		return apiErr
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("embed: decode response: %w", err)
	}
	return nil
}

// transportError is a network failure (retryable unless ctx is done).
type transportError struct {
	err   error
	cause error
}

func (e *transportError) Error() string { return "embed: request failed: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.cause }

// retryAfterError carries the server's Retry-After hint.
type retryAfterError struct {
	*APIError
	after time.Duration
}

func (e *retryAfterError) Unwrap() error { return e.APIError }

func retryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Temporary()
	}
	var te *transportError
	return errors.As(err, &te) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func (c *httpClient) backoff(attempt int, lastErr error) time.Duration {
	var ra *retryAfterError
	if errors.As(lastErr, &ra) && ra.after > 0 {
		return min(ra.after, c.retry.max)
	}
	d := c.retry.base << (attempt - 1)
	if d <= 0 || d > c.retry.max {
		d = c.retry.max
	}
	// ±20% jitter so parallel workers do not retry in lockstep.
	jitter := time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
	return jitter
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// redact removes the API key from s.
func (c *httpClient) redact(s string) string {
	if c.secret == "" {
		return s
	}
	return strings.ReplaceAll(s, c.secret, "[redacted]")
}

// errorMessage extracts {"error":{"message":…}} / {"error":"…"} or returns the
// (truncated) body.
func errorMessage(raw []byte) string {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &body) == nil && len(body.Error) > 0 {
		var obj struct {
			Message string `json:"message"`
		}
		var str string
		switch {
		case json.Unmarshal(body.Error, &obj) == nil && obj.Message != "":
			msg = obj.Message
		case json.Unmarshal(body.Error, &str) == nil:
			msg = str
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if r := []rune(msg); len(r) > maxErrorBody {
		msg = string(r[:maxErrorBody]) + "…"
	}
	return msg
}

// noteDims records the dimension of the first successful response and
// rejects vectors of another size.
func (c *httpClient) noteDims(vecs [][]float32) error {
	for _, v := range vecs {
		if len(v) == 0 {
			return errors.New("embed: provider returned an empty vector")
		}
		cur := c.dims.Load()
		if cur == 0 {
			c.dims.CompareAndSwap(0, int64(len(v)))
			cur = c.dims.Load()
		}
		if int64(len(v)) != cur {
			return fmt.Errorf("embed: provider returned %d dimensions, expected %d", len(v), cur)
		}
	}
	return nil
}
