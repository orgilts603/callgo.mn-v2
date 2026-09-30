package live

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// wsClient is one connected browser.
type wsClient struct {
	orgID uuid.UUID
	send  chan []byte
	done  chan struct{}
	once  sync.Once
}

func newWSClient(orgID uuid.UUID, buffer int) *wsClient {
	return &wsClient{orgID: orgID, send: make(chan []byte, buffer), done: make(chan struct{})}
}

func (c *wsClient) deliver(_ domain.Event, raw []byte) bool { return c.enqueue(raw) }

// enqueue queues a frame without blocking; false means the buffer is full.
func (c *wsClient) enqueue(raw []byte) bool {
	select {
	case <-c.done:
		return true // already closing; not "slow"
	default:
	}
	select {
	case c.send <- raw:
		return true
	default:
		return false
	}
}

func (c *wsClient) close() { c.once.Do(func() { close(c.done) }) }

// clientMessage is a browser → server message.
type clientMessage struct {
	Type   string `json:"type"`
	CallID string `json:"callId,omitempty"`
}

var pongFrame = []byte(`{"type":"pong"}`)

// ServeWS upgrades the request to a WebSocket for the organisation and
// streams its events until the client disconnects or the hub closes. The
// handler blocks for the lifetime of the connection. Authentication and the
// resolution of orgID are the caller's job (see docs/API.md). initial supplies
// the active calls for the hello message and may be nil.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, initial func(ctx context.Context) []domain.Call) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "live hub closed", http.StatusServiceUnavailable)
		return
	}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Debug().Err(err).Msg("websocket upgrade failed") // Upgrade already replied
		return
	}

	c := newWSClient(orgID, h.opts.SendBuffer)
	// Register before loading the active calls so no event is lost between
	// the snapshot and the subscription (duplicates are de-duplicated by id).
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return
	}
	h.addLocked(orgID, c)
	h.mu.Unlock()
	defer h.remove(orgID, c)

	calls := []domain.Call{}
	if initial != nil {
		ctx, cancel := context.WithTimeout(r.Context(), initialLoadTimeout)
		if got := initial(ctx); got != nil {
			calls = got
		}
		cancel()
	}
	hello, err := json.Marshal(domain.Event{
		ID:      uuid.NewString(),
		Type:    domain.EventSystem,
		OrgID:   orgID,
		At:      time.Now().UTC(),
		Payload: map[string]any{"hello": true, "activeCalls": calls},
	})
	if err != nil {
		h.log.Error().Err(err).Msg("marshal hello")
		_ = conn.Close()
		return
	}

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		h.writePump(conn, c, hello)
	}()

	h.readPump(conn, c)
	c.close()
	<-writerDone
}

func (h *Hub) writePump(conn *websocket.Conn, c *wsClient, first []byte) {
	ticker := time.NewTicker(h.opts.PingInterval)
	defer func() {
		ticker.Stop()
		_ = conn.Close()
	}()

	write := func(msgType int, data []byte) error {
		_ = conn.SetWriteDeadline(time.Now().Add(h.opts.WriteTimeout))
		return conn.WriteMessage(msgType, data)
	}
	if err := write(websocket.TextMessage, first); err != nil {
		return
	}
	for {
		select {
		case msg := <-c.send:
			if err := write(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := write(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second))
			return
		case <-h.ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutting down"),
				time.Now().Add(time.Second))
			return
		}
	}
}

func (h *Hub) readPump(conn *websocket.Conn, c *wsClient) {
	// A live client answers pings with pongs (or sends data) well within this.
	idle := 2*h.opts.PingInterval + h.opts.WriteTimeout
	conn.SetReadLimit(maxClientMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(idle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(idle))
	})
	for {
		typ, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseNoStatusReceived) &&
				!errors.Is(err, net.ErrClosed) {
				h.log.Debug().Err(err).Str("orgId", c.orgID.String()).Msg("websocket read ended")
			}
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(idle))
		if typ != websocket.TextMessage {
			continue
		}
		var msg clientMessage
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "ping":
			if !c.enqueue(pongFrame) {
				h.log.Warn().Str("orgId", c.orgID.String()).Msg("live: client send buffer full; disconnecting")
				return
			}
		case "subscribe":
			// Every client already receives all events of its organisation.
		}
	}
}
