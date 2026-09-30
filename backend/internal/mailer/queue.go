package mailer

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// ErrQueueFull is returned by Queue.Send when the buffer is full, and
// ErrQueueClosed after Close.
var (
	ErrQueueFull   = errors.New("mailer: queue full")
	ErrQueueClosed = errors.New("mailer: queue closed")
)

type job struct {
	to, subject, text, html string
}

// Queue decouples request handlers from a slow mail relay: Send enqueues and
// returns immediately, one background worker delivers through the inner
// mailer with a per-message timeout and up to Attempts tries.
type Queue struct {
	inner domain.Mailer
	log   zerolog.Logger

	// Attempts per message (default 3), RetryDelay between them (default
	// 5 s, multiplied by the attempt number) and Timeout per attempt
	// (default DefaultTimeout). Set before the first Send.
	Attempts   int
	RetryDelay time.Duration
	Timeout    time.Duration

	mu     sync.Mutex
	closed bool
	ch     chan job
	done   chan struct{}
	stop   chan struct{}
}

var _ domain.Mailer = (*Queue)(nil)

// NewQueue starts the delivery worker. size is the buffer capacity
// (default 256). Call Close on shutdown to drain the queue.
func NewQueue(inner domain.Mailer, log zerolog.Logger, size int) *Queue {
	if size <= 0 {
		size = 256
	}
	q := &Queue{
		inner: inner, log: log.With().Str("component", "mailer-queue").Logger(),
		Attempts: 3, RetryDelay: 5 * time.Second, Timeout: DefaultTimeout,
		ch: make(chan job, size), done: make(chan struct{}), stop: make(chan struct{}),
	}
	go q.run()
	return q
}

// Send enqueues the message. The context only bounds enqueueing.
func (q *Queue) Send(ctx context.Context, to, subject, textBody, htmlBody string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrQueueClosed
	}
	select {
	case q.ch <- job{to, subject, textBody, htmlBody}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrQueueFull
	}
}

// Close stops accepting messages and waits until queued ones are delivered
// or ctx expires (remaining retries are then abandoned).
func (q *Queue) Close(ctx context.Context) error {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
	q.mu.Unlock()
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		q.stopOnce()
		return ctx.Err()
	}
}

func (q *Queue) stopOnce() {
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-q.stop:
	default:
		close(q.stop)
	}
}

func (q *Queue) run() {
	defer close(q.done)
	for j := range q.ch {
		q.deliver(j)
	}
}

func (q *Queue) deliver(j job) {
	attempts := max(q.Attempts, 1)
	for i := 1; i <= attempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), q.Timeout)
		err := q.inner.Send(ctx, j.to, j.subject, j.text, j.html)
		cancel()
		if err == nil {
			return
		}
		ev := q.log.Warn()
		if i == attempts || errors.Is(err, domain.ErrInvalid) {
			ev = q.log.Error()
		}
		ev.Err(err).Str("to", j.to).Str("subject", j.subject).Int("attempt", i).Msg("send email failed")
		if errors.Is(err, domain.ErrInvalid) || i == attempts {
			return
		}
		select {
		case <-time.After(q.RetryDelay * time.Duration(i)):
		case <-q.stop:
			return
		}
	}
}
