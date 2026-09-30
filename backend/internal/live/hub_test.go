package live

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// wsEvent is the loose wire shape of a server message.
type wsEvent struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	OrgID   uuid.UUID       `json:"orgId"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload"`
}

func newTestHub(t *testing.T, opts HubOptions) *Hub {
	t.Helper()
	h := NewHub(zerolog.Nop(), opts)
	t.Cleanup(h.Close)
	return h
}

// serve mounts ServeWS for the org given in the ?org= query parameter.
func serve(t *testing.T, h *Hub, initial func(ctx context.Context) []domain.Call) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		org, err := uuid.Parse(r.URL.Query().Get("org"))
		if err != nil {
			http.Error(w, "bad org", http.StatusBadRequest)
			return
		}
		h.ServeWS(w, r, org, initial)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dial(t *testing.T, srv *httptest.Server, org uuid.UUID) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?org=" + org.String()
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func readEvent(t *testing.T, c *websocket.Conn) wsEvent {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	var ev wsEvent
	require.NoError(t, c.ReadJSON(&ev))
	return ev
}

func waitClients(t *testing.T, h *Hub, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return h.Stats().Clients == n }, 3*time.Second, 5*time.Millisecond)
}

func TestHubFanOutToMultipleClients(t *testing.T) {
	h := newTestHub(t, HubOptions{})
	srv := serve(t, h, nil)
	org := uuid.New()
	conns := []*websocket.Conn{dial(t, srv, org), dial(t, srv, org), dial(t, srv, org)}
	for _, c := range conns {
		assert.Equal(t, "system", readEvent(t, c).Type) // hello
	}
	waitClients(t, h, 3)

	callID := uuid.New()
	h.Publish(context.Background(), domain.Event{
		Type: domain.EventTranscriptFinal, OrgID: org, CallID: &callID,
		Payload: map[string]any{"text": "сайн байна уу"},
	})
	for _, c := range conns {
		ev := readEvent(t, c)
		assert.Equal(t, "transcript.final", ev.Type)
		assert.Equal(t, org, ev.OrgID)
		assert.NotEmpty(t, ev.ID, "hub assigns an id")
		assert.False(t, ev.At.IsZero(), "hub assigns a timestamp")
		assert.JSONEq(t, `{"text":"сайн байна уу"}`, string(ev.Payload))
	}
}

func TestHubOrgIsolation(t *testing.T) {
	h := newTestHub(t, HubOptions{})
	srv := serve(t, h, nil)
	orgA, orgB := uuid.New(), uuid.New()
	a, b := dial(t, srv, orgA), dial(t, srv, orgB)
	readEvent(t, a)
	readEvent(t, b)
	waitClients(t, h, 2)
	assert.Equal(t, 2, h.Stats().Orgs)

	h.Publish(context.Background(), domain.Event{Type: domain.EventCallStarted, OrgID: orgA, ID: "a-1"})
	h.Publish(context.Background(), domain.Event{Type: domain.EventCallStarted, OrgID: orgB, ID: "b-1"})

	assert.Equal(t, "a-1", readEvent(t, a).ID)
	assert.Equal(t, "b-1", readEvent(t, b).ID)

	// Neither connection has anything else queued.
	for _, c := range []*websocket.Conn{a, b} {
		require.NoError(t, c.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
		_, _, err := c.ReadMessage()
		require.Error(t, err)
	}
}

func TestServeWSHelloIncludesActiveCalls(t *testing.T) {
	h := newTestHub(t, HubOptions{})
	org := uuid.New()
	call := domain.Call{ID: uuid.New(), OrgID: org, Status: domain.StatusActive, Direction: domain.DirectionInbound, FromNumber: "+97699112233"}
	srv := serve(t, h, func(ctx context.Context) []domain.Call {
		require.NotNil(t, ctx)
		return []domain.Call{call}
	})
	ev := readEvent(t, dial(t, srv, org))
	assert.Equal(t, "system", ev.Type)
	var p struct {
		Hello       bool          `json:"hello"`
		ActiveCalls []domain.Call `json:"activeCalls"`
	}
	require.NoError(t, json.Unmarshal(ev.Payload, &p))
	assert.True(t, p.Hello)
	require.Len(t, p.ActiveCalls, 1)
	assert.Equal(t, call.ID, p.ActiveCalls[0].ID)
	assert.Equal(t, "+97699112233", p.ActiveCalls[0].FromNumber)
}

func TestServeWSHelloWithoutInitialHasEmptyList(t *testing.T) {
	h := newTestHub(t, HubOptions{})
	srv := serve(t, h, nil)
	ev := readEvent(t, dial(t, srv, uuid.New()))
	assert.Equal(t, "system", ev.Type)
	assert.JSONEq(t, `{"hello":true,"activeCalls":[]}`, string(ev.Payload))

	// A nil slice from initial is also rendered as [].
	srv2 := serve(t, h, func(context.Context) []domain.Call { return nil })
	ev = readEvent(t, dial(t, srv2, uuid.New()))
	assert.JSONEq(t, `{"hello":true,"activeCalls":[]}`, string(ev.Payload))
}

func TestServeWSPingPongAndSubscribe(t *testing.T) {
	h := newTestHub(t, HubOptions{})
	srv := serve(t, h, nil)
	c := dial(t, srv, uuid.New())
	readEvent(t, c) // hello

	require.NoError(t, c.WriteMessage(websocket.TextMessage, []byte(`{"type":"subscribe","callId":"`+uuid.NewString()+`"}`)))
	require.NoError(t, c.WriteMessage(websocket.TextMessage, []byte(`not json`)))
	require.NoError(t, c.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)))
	ev := readEvent(t, c)
	assert.Equal(t, "pong", ev.Type)
}

func TestServeWSSendsPingFrames(t *testing.T) {
	h := newTestHub(t, HubOptions{PingInterval: 30 * time.Millisecond})
	srv := serve(t, h, nil)
	c := dial(t, srv, uuid.New())
	var pings atomic.Int32
	c.SetPingHandler(func(string) error { pings.Add(1); return nil })
	readEvent(t, c) // hello
	// Reading drives the ping handler.
	require.NoError(t, c.SetReadDeadline(time.Now().Add(400*time.Millisecond)))
	_, _, _ = c.ReadMessage()
	assert.GreaterOrEqual(t, pings.Load(), int32(2))
}

func TestServeWSClientDisconnectUnregisters(t *testing.T) {
	h := newTestHub(t, HubOptions{})
	srv := serve(t, h, nil)
	c := dial(t, srv, uuid.New())
	readEvent(t, c)
	waitClients(t, h, 1)
	require.NoError(t, c.Close())
	waitClients(t, h, 0)
	assert.Equal(t, 0, h.Stats().Orgs)
}

func TestSlowWebSocketClientIsDropped(t *testing.T) {
	h := newTestHub(t, HubOptions{SendBuffer: 4})
	srv := serve(t, h, nil)
	org := uuid.New()
	slow := dial(t, srv, org) // never reads after hello
	fast := dial(t, srv, org)
	readEvent(t, slow)
	readEvent(t, fast)
	waitClients(t, h, 2)

	big := strings.Repeat("x", 256*1024)
	var fastGot atomic.Int32
	go func() {
		for {
			if err := fast.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return
			}
			if _, _, err := fast.ReadMessage(); err != nil {
				return
			}
			fastGot.Add(1)
		}
	}()

	// Fill the socket buffers of the slow client until the hub gives up on it.
	require.Eventually(t, func() bool {
		h.Publish(context.Background(), domain.Event{Type: domain.EventSystem, OrgID: org, Payload: big})
		time.Sleep(time.Millisecond)
		return h.Stats().Clients == 1
	}, 10*time.Second, time.Millisecond, "slow client should be disconnected")

	// The fast client is unaffected and keeps receiving.
	before := fastGot.Load()
	h.Publish(context.Background(), domain.Event{Type: domain.EventSystem, OrgID: org, Payload: "still-here"})
	require.Eventually(t, func() bool { return fastGot.Load() > before }, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, 1, h.Stats().Clients)
}

func TestHubCloseDisconnectsClients(t *testing.T) {
	h := NewHub(zerolog.Nop(), HubOptions{})
	srv := serve(t, h, nil)
	c := dial(t, srv, uuid.New())
	readEvent(t, c)
	sub, _ := h.Subscribe(uuid.New())
	waitClients(t, h, 2)

	h.Close()
	h.Close() // idempotent

	require.NoError(t, c.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err := c.ReadMessage()
	require.Error(t, err)
	assert.True(t, websocket.IsCloseError(err, websocket.CloseGoingAway), "got %v", err)
	_, ok := <-sub
	assert.False(t, ok, "subscriber channel closed")
	assert.Equal(t, HubStats{}, h.Stats())

	// New connections are refused after Close.
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/?org=" + uuid.NewString()
	_, resp, err := websocket.DefaultDialer.Dial(u, nil)
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	// Publish after Close is a no-op, not a panic.
	h.Publish(context.Background(), domain.Event{Type: domain.EventSystem})
}

func TestSubscribeAndCancel(t *testing.T) {
	h := newTestHub(t, HubOptions{})
	org, other := uuid.New(), uuid.New()
	ch, cancel := h.Subscribe(org)
	assert.Equal(t, HubStats{Clients: 1, Orgs: 1}, h.Stats())

	h.Publish(context.Background(), domain.Event{Type: domain.EventCallStarted, OrgID: org, ID: "e1"})
	h.Publish(context.Background(), domain.Event{Type: domain.EventCallStarted, OrgID: other, ID: "x"})
	select {
	case ev := <-ch:
		assert.Equal(t, "e1", ev.ID)
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event from another org: %+v", ev)
	default:
	}

	cancel()
	cancel() // idempotent
	_, ok := <-ch
	assert.False(t, ok)
	assert.Equal(t, HubStats{}, h.Stats())
	h.Publish(context.Background(), domain.Event{Type: domain.EventCallStarted, OrgID: org}) // must not panic
}

func TestSlowSubscriberIsClosed(t *testing.T) {
	h := newTestHub(t, HubOptions{SendBuffer: 2})
	org := uuid.New()
	slow, _ := h.Subscribe(org)
	fast, cancelFast := h.Subscribe(org)
	defer cancelFast()

	for i := 0; i < 3; i++ {
		h.Publish(context.Background(), domain.Event{Type: domain.EventSystem, OrgID: org})
		<-fast
	}
	// The slow channel got its 2 buffered events, then was closed.
	n := 0
	for range slow {
		n++
	}
	assert.Equal(t, 2, n)
	assert.Equal(t, 1, h.Stats().Clients)
}

func TestPublishStampsIDAndTime(t *testing.T) {
	h := newTestHub(t, HubOptions{})
	org := uuid.New()
	ch, cancel := h.Subscribe(org)
	defer cancel()
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)

	h.Publish(context.Background(), domain.Event{Type: domain.EventSystem, OrgID: org})
	h.Publish(context.Background(), domain.Event{Type: domain.EventSystem, OrgID: org, ID: "keep", At: at})

	a, b := <-ch, <-ch
	_, err := uuid.Parse(a.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), a.At, 2*time.Second)
	assert.Equal(t, "keep", b.ID)
	assert.True(t, at.Equal(b.At))
}

func TestRecentRingBuffer(t *testing.T) {
	h := newTestHub(t, HubOptions{ReplayBuffer: 5})
	org, other := uuid.New(), uuid.New()
	assert.Empty(t, h.Recent(org, 10))

	for i := 1; i <= 3; i++ {
		h.Publish(context.Background(), domain.Event{Type: domain.EventSystem, OrgID: org, ID: string(rune('a' + i - 1))})
	}
	ids := func(evs []domain.Event) string {
		var sb strings.Builder
		for _, e := range evs {
			sb.WriteString(e.ID)
		}
		return sb.String()
	}
	assert.Equal(t, "abc", ids(h.Recent(org, 10)))
	assert.Equal(t, "bc", ids(h.Recent(org, 2)))
	assert.Empty(t, h.Recent(org, 0))

	for _, id := range []string{"d", "e", "f", "g"} { // wraps the 5-slot ring
		h.Publish(context.Background(), domain.Event{Type: domain.EventSystem, OrgID: org, ID: id})
	}
	assert.Equal(t, "cdefg", ids(h.Recent(org, 100)))
	assert.Equal(t, "fg", ids(h.Recent(org, 2)))
	assert.Empty(t, h.Recent(other, 5), "rings are per organisation")
}

func TestHubConcurrentUse(t *testing.T) {
	h := newTestHub(t, HubOptions{SendBuffer: 1024})
	srv := serve(t, h, nil)
	org := uuid.New()
	c := dial(t, srv, org)
	readEvent(t, c)

	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				ch, cancel := h.Subscribe(org)
				h.Publish(context.Background(), domain.Event{Type: domain.EventSystem, OrgID: org})
				_ = h.Recent(org, 10)
				_ = h.Stats()
				cancel()
				_ = ch
			}
		}()
	}
	for g := 0; g < 4; g++ {
		<-done
	}
}
