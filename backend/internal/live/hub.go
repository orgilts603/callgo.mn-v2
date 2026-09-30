// Package live implements the in-process real-time event hub (fan-out of
// domain.Event to browsers over WebSocket and to in-process subscribers) and
// a call simulator used to demo the Live Desk without a telephony trunk.
package live

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Defaults applied by NewHub for zero-valued options.
const (
	DefaultSendBuffer     = 256
	DefaultPingInterval   = 25 * time.Second
	DefaultWriteTimeout   = 10 * time.Second
	DefaultReplayBuffer   = 200
	maxClientMessageBytes = 64 * 1024
	initialLoadTimeout    = 10 * time.Second
)

// HubOptions tunes a Hub. Zero values select the documented defaults.
type HubOptions struct {
	// SendBuffer is the per-client outbound queue size. A client whose queue
	// is full is disconnected (default 256).
	SendBuffer int
	// PingInterval is how often a WebSocket ping frame is sent (default 25s).
	PingInterval time.Duration
	// WriteTimeout bounds every socket write (default 10s).
	WriteTimeout time.Duration
	// ReplayBuffer is the number of recent events kept per organisation for
	// Recent (default 200).
	ReplayBuffer int
	// CheckOrigin validates the WebSocket handshake Origin. When nil every
	// origin is accepted (the API authenticates with a token, not cookies).
	CheckOrigin func(r *http.Request) bool
}

// HubStats is a snapshot of connected consumers.
type HubStats struct {
	// Clients counts WebSocket clients plus in-process subscribers.
	Clients int `json:"clients"`
	// Orgs counts organisations that currently have at least one consumer.
	Orgs int `json:"orgs"`
}

// subscriber is one consumer registered with the hub. deliver must never
// block; it reports false when the consumer's buffer is full. close is only
// called by the hub.
type subscriber interface {
	deliver(ev domain.Event, raw []byte) bool
	close()
}

// Hub is an in-process event bus with per-organisation fan-out and replay
// ring buffers. It implements domain.EventBus.
type Hub struct {
	log  zerolog.Logger
	opts HubOptions

	upgrader websocket.Upgrader

	mu     sync.Mutex
	orgs   map[uuid.UUID]map[subscriber]struct{}
	rings  map[uuid.UUID]*ring
	closed bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var _ domain.EventBus = (*Hub)(nil)

// NewHub builds a Hub. Call Close on shutdown.
func NewHub(log zerolog.Logger, opts HubOptions) *Hub {
	if opts.SendBuffer <= 0 {
		opts.SendBuffer = DefaultSendBuffer
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = DefaultPingInterval
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = DefaultWriteTimeout
	}
	if opts.ReplayBuffer <= 0 {
		opts.ReplayBuffer = DefaultReplayBuffer
	}
	checkOrigin := opts.CheckOrigin
	if checkOrigin == nil {
		checkOrigin = func(*http.Request) bool { return true }
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		log:  log.With().Str("component", "live.hub").Logger(),
		opts: opts,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 4096,
			CheckOrigin:     checkOrigin,
		},
		orgs:   make(map[uuid.UUID]map[subscriber]struct{}),
		rings:  make(map[uuid.UUID]*ring),
		ctx:    ctx,
		cancel: cancel,
	}
}

// Publish stamps ev (ID, At), records it in the organisation's ring buffer and
// fans it out without blocking. Consumers whose buffer is full are
// disconnected. The context is accepted for interface compatibility; delivery
// is purely in-memory and never blocks on it.
func (h *Hub) Publish(_ context.Context, ev domain.Event) {
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		h.log.Error().Err(err).Str("type", string(ev.Type)).Msg("marshal event; not sent to websocket clients")
		raw = nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	r := h.rings[ev.OrgID]
	if r == nil {
		r = newRing(h.opts.ReplayBuffer)
		h.rings[ev.OrgID] = r
	}
	r.push(ev)

	subs := h.orgs[ev.OrgID]
	for s := range subs {
		if _, isWS := s.(*wsClient); isWS && raw == nil {
			continue
		}
		if s.deliver(ev, raw) {
			continue
		}
		h.log.Warn().
			Str("orgId", ev.OrgID.String()).
			Str("type", string(ev.Type)).
			Msg("live: consumer too slow (send buffer full); disconnecting")
		delete(subs, s)
		s.close()
	}
	if len(subs) == 0 {
		delete(h.orgs, ev.OrgID)
	}
}

// Recent returns up to n most recent events of the organisation, oldest first.
func (h *Hub) Recent(orgID uuid.UUID, n int) []domain.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rings[orgID]
	if r == nil || n <= 0 {
		return []domain.Event{}
	}
	return r.last(n)
}

// Stats reports how many consumers and organisations are connected.
func (h *Hub) Stats() HubStats {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := HubStats{}
	for _, subs := range h.orgs {
		if len(subs) == 0 {
			continue
		}
		st.Orgs++
		st.Clients += len(subs)
	}
	return st
}

// memSub is an in-process subscriber.
type memSub struct {
	ch   chan domain.Event
	once sync.Once
}

func (m *memSub) deliver(ev domain.Event, _ []byte) bool {
	select {
	case m.ch <- ev:
		return true
	default:
		return false
	}
}

func (m *memSub) close() { m.once.Do(func() { close(m.ch) }) }

// Subscribe registers an in-process consumer for the organisation. The
// returned channel is buffered (HubOptions.SendBuffer); a consumer that does
// not keep up has its channel closed, exactly like a slow WebSocket client.
// The channel is also closed by cancel and by Hub.Close. cancel is idempotent.
func (h *Hub) Subscribe(orgID uuid.UUID) (<-chan domain.Event, func()) {
	m := &memSub{ch: make(chan domain.Event, h.opts.SendBuffer)}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		m.close()
		return m.ch, func() {}
	}
	h.addLocked(orgID, m)
	h.mu.Unlock()
	return m.ch, func() { h.remove(orgID, m) }
}

func (h *Hub) addLocked(orgID uuid.UUID, s subscriber) {
	subs := h.orgs[orgID]
	if subs == nil {
		subs = make(map[subscriber]struct{})
		h.orgs[orgID] = subs
	}
	subs[s] = struct{}{}
}

// remove unregisters s (if still registered) and closes it.
func (h *Hub) remove(orgID uuid.UUID, s subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.orgs[orgID]
	if _, ok := subs[s]; ok {
		delete(subs, s)
		if len(subs) == 0 {
			delete(h.orgs, orgID)
		}
	}
	// close is idempotent for both subscriber kinds.
	s.close()
}

// Close disconnects every consumer, rejects new ones and waits for WebSocket
// handlers to return. It is safe to call more than once.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		h.wg.Wait()
		return
	}
	h.closed = true
	h.cancel()
	for orgID, subs := range h.orgs {
		for s := range subs {
			s.close()
		}
		delete(h.orgs, orgID)
	}
	h.mu.Unlock()
	h.wg.Wait()
}

// ring is a fixed-capacity circular buffer of events.
type ring struct {
	buf   []domain.Event
	start int
	n     int
}

func newRing(capacity int) *ring { return &ring{buf: make([]domain.Event, capacity)} }

func (r *ring) push(ev domain.Event) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = ev
		r.n++
		return
	}
	r.buf[r.start] = ev
	r.start = (r.start + 1) % len(r.buf)
}

// last returns the newest n events in chronological order.
func (r *ring) last(n int) []domain.Event {
	if n > r.n {
		n = r.n
	}
	out := make([]domain.Event, n)
	for i := 0; i < n; i++ {
		out[i] = r.buf[(r.start+r.n-n+i)%len(r.buf)]
	}
	return out
}
