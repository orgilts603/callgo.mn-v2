package middleware

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter is a per-client-IP token bucket.
type RateLimiter struct {
	rps   float64
	burst float64
	idle  time.Duration
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket

	stop     chan struct{}
	stopOnce sync.Once
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter creates a limiter allowing rps requests per second per client
// IP with the given burst, and starts a goroutine that evicts idle buckets.
// Call Stop to end that goroutine.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	if burst < 1 {
		burst = 1
	}
	// A bucket that has been idle long enough to fully refill is equivalent to
	// a fresh one, so it can be dropped.
	idle := 5 * time.Minute
	if rps > 0 {
		if refill := time.Duration(float64(burst) / rps * float64(time.Second)); refill > idle {
			idle = refill
		}
	}
	rl := &RateLimiter{
		rps:     rps,
		burst:   float64(burst),
		idle:    idle,
		now:     time.Now,
		buckets: make(map[string]*bucket),
		stop:    make(chan struct{}),
	}
	go rl.cleanupLoop(time.Minute)
	return rl
}

// Stop terminates the cleanup goroutine. It is safe to call more than once.
func (rl *RateLimiter) Stop() { rl.stopOnce.Do(func() { close(rl.stop) }) }

func (rl *RateLimiter) cleanupLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			rl.cleanup()
		case <-rl.stop:
			return
		}
	}
}

func (rl *RateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := rl.now().Add(-rl.idle)
	for k, b := range rl.buckets {
		if b.last.Before(cutoff) {
			delete(rl.buckets, k)
		}
	}
}

// size returns the number of tracked clients (for tests).
func (rl *RateLimiter) size() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.buckets)
}

// allow takes a token for key. When none is available it reports how long
// until one is.
func (rl *RateLimiter) allow(key string) (ok bool, retryAfter time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	b, found := rl.buckets[key]
	if !found {
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	} else if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = math.Min(rl.burst, b.tokens+elapsed.Seconds()*rl.rps)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	if rl.rps <= 0 {
		return false, time.Hour
	}
	return false, time.Duration((1 - b.tokens) / rl.rps * float64(time.Second))
}

// Middleware returns the chi-compatible handler wrapper. Rejected requests get
// 429 with a Retry-After header and the standard JSON error body.
func (rl *RateLimiter) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retry := rl.allow(clientIP(r))
			if !ok {
				secs := int(math.Ceil(retry.Seconds()))
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit limits each client IP to rps requests per second with the given
// burst. The eviction goroutine lives for the life of the process; use
// NewRateLimiter directly if you need to stop it.
func RateLimit(rps float64, burst int) func(http.Handler) http.Handler {
	return NewRateLimiter(rps, burst).Middleware()
}
