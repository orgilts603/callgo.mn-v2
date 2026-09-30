package livekit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// eventLog collects OnEvent callbacks.
type eventLog struct {
	mu  sync.Mutex
	evs map[string][]string
}

func newEventLog() *eventLog { return &eventLog{evs: map[string][]string{}} }

func (l *eventLog) on(room, evt string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs[room] = append(l.evs[room], evt)
}

func (l *eventLog) get(room string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.evs[room]...)
}

const (
	testRing = 20 * time.Millisecond
	waitFor  = 2 * time.Second
	tick     = 2 * time.Millisecond
)

func mockReq(to string, wait bool) domain.OutboundCallRequest {
	return domain.OutboundCallRequest{CallID: uuid.New(), ToNumber: to, WaitUntilAnswered: wait}
}

func TestDefaultMockOutcome(t *testing.T) {
	assert.Equal(t, MockNoAnswer, DefaultMockOutcome("+97699112230"))
	assert.Equal(t, MockBusy, DefaultMockOutcome("+97699112231"))
	assert.Equal(t, MockAnswered, DefaultMockOutcome("+97699112232"))
	assert.Equal(t, MockAnswered, DefaultMockOutcome("+97699112239 "))
	assert.Equal(t, MockFailed, DefaultMockOutcome(""))
}

func TestMockDialOutcomesBlocking(t *testing.T) {
	log := newEventLog()
	m := NewMock(MockOptions{RingDelay: testRing, OnEvent: log.on})
	t.Cleanup(m.Close)
	ctx := context.Background()

	for _, tc := range []struct {
		to     string
		status domain.CallStatus
		events []string
	}{
		{"+97699112232", domain.StatusActive, []string{"ringing", "answered"}},
		{"+97699112231", domain.StatusBusy, []string{"ringing", "busy"}},
		{"+97699112230", domain.StatusNoAnswer, []string{"ringing", "no_answer"}},
	} {
		req := mockReq(tc.to, true)
		start := time.Now()
		res, err := m.Dial(ctx, req)
		require.NoError(t, err, tc.to)
		assert.GreaterOrEqual(t, time.Since(start), testRing, "rang first")
		assert.Equal(t, tc.status, DialStatus(res), tc.to)
		room := RoomNameForCall(req.CallID)
		assert.Equal(t, tc.events, log.get(room), tc.to)
		if tc.status == domain.StatusActive {
			assert.NotEmpty(t, res.ParticipantID)
			assert.NotEmpty(t, res.SIPCallID)
		}
	}

	rooms, err := m.ListActiveRooms(ctx)
	require.NoError(t, err)
	require.Len(t, rooms, 1, "only the answered call stays up")

	require.NoError(t, m.Hangup(ctx, rooms[0]))
	assert.Eventually(t, func() bool {
		evs := log.get(rooms[0])
		return len(evs) == 3 && evs[2] == "ended"
	}, waitFor, tick)
	rooms, _ = m.ListActiveRooms(ctx)
	assert.Empty(t, rooms)

	calls := m.Calls()
	require.Len(t, calls, 3)
	assert.Equal(t, MockAnswered, calls[0].Outcome)
	assert.Equal(t, "ended", calls[0].State)
	assert.False(t, calls[0].EndedAt.IsZero())
	assert.Equal(t, MockBusy, calls[1].Outcome)
	assert.Equal(t, "busy", calls[1].State)
}

func TestMockNonBlockingAndCustomOutcome(t *testing.T) {
	log := newEventLog()
	m := NewMock(MockOptions{
		RingDelay:    testRing,
		CallDuration: 30 * time.Millisecond,
		Outcome:      func(string) MockOutcome { return MockAnswered },
		OnEvent:      log.on,
	})
	t.Cleanup(m.Close)
	req := mockReq("+97699112231", false) // would be busy with the default rule
	req.RoomName = "call-custom"
	res, err := m.Dial(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, res.Answered)
	assert.Equal(t, domain.StatusRinging, DialStatus(res))

	assert.Eventually(t, func() bool {
		evs := log.get("call-custom")
		return len(evs) == 3
	}, waitFor, tick)
	assert.Equal(t, []string{"ringing", "answered", "ended"}, log.get("call-custom"), "auto-ended after CallDuration")
}

func TestMockHangupWhileRinging(t *testing.T) {
	log := newEventLog()
	m := NewMock(MockOptions{RingDelay: time.Hour, OnEvent: log.on})
	t.Cleanup(m.Close)
	ctx := context.Background()
	req := mockReq("+97699112232", true)
	room := RoomNameForCall(req.CallID)

	done := make(chan domain.OutboundCallResult, 1)
	go func() {
		res, err := m.Dial(ctx, req)
		assert.NoError(t, err)
		done <- res
	}()
	assert.Eventually(t, func() bool { return len(log.get(room)) == 1 }, waitFor, tick)
	rooms, _ := m.ListActiveRooms(ctx)
	assert.Equal(t, []string{room}, rooms)

	require.NoError(t, m.Hangup(ctx, room))
	select {
	case res := <-done:
		assert.False(t, res.Answered)
		assert.Equal(t, domain.StatusFailed, DialStatus(res))
	case <-time.After(waitFor):
		t.Fatal("Dial did not return after Hangup")
	}
	assert.Eventually(t, func() bool { return len(log.get(room)) == 2 }, waitFor, tick)
	assert.Equal(t, []string{"ringing", "ended"}, log.get(room))
	require.NoError(t, m.Hangup(ctx, room), "idempotent")
}

func TestMockDialContextCancel(t *testing.T) {
	m := NewMock(MockOptions{RingDelay: time.Hour})
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res, err := m.Dial(ctx, mockReq("+97699112232", true))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, domain.StatusFailed, DialStatus(res))
	assert.Eventually(t, func() bool {
		rooms, _ := m.ListActiveRooms(context.Background())
		return len(rooms) == 0
	}, waitFor, tick)
}

func TestMockHangupFromCallback(t *testing.T) {
	var m *Mock
	ended := make(chan struct{})
	m = NewMock(MockOptions{RingDelay: testRing, OnEvent: func(room, evt string) {
		switch evt {
		case MockEventAnswered:
			_ = m.Hangup(context.Background(), room) // must not deadlock
		case MockEventEnded:
			close(ended)
		}
	}})
	t.Cleanup(m.Close)
	_, err := m.Dial(context.Background(), mockReq("+97699112232", false))
	require.NoError(t, err)
	select {
	case <-ended:
	case <-time.After(waitFor):
		t.Fatal("call did not end")
	}
}

func TestMockTransferConflictAndClose(t *testing.T) {
	m := NewMock(MockOptions{RingDelay: testRing})
	ctx := context.Background()
	req := mockReq("+97699112232", true)
	_, err := m.Dial(ctx, req)
	require.NoError(t, err)
	room := RoomNameForCall(req.CallID)

	_, err = m.Dial(ctx, req)
	require.ErrorIs(t, err, domain.ErrConflict, "same room twice")

	require.NoError(t, m.TransferCall(ctx, room, "", "+97611112222"))
	assert.Equal(t, "tel:+97611112222", m.Calls()[0].TransferredTo)
	require.ErrorIs(t, m.TransferCall(ctx, "call-nope", "", "+976"), domain.ErrNotFound)
	require.ErrorIs(t, m.TransferCall(ctx, room, "", ""), domain.ErrInvalid)

	_, err = m.Dial(ctx, mockReq("", true))
	require.ErrorIs(t, err, domain.ErrInvalid)

	m.Close()
	rooms, _ := m.ListActiveRooms(ctx)
	assert.Empty(t, rooms)
	assert.Equal(t, "ended", m.Calls()[0].State)
	_, err = m.Dial(ctx, mockReq("+97699112232", false))
	require.Error(t, err, "closed")
}

func TestMockConcurrentDials(t *testing.T) {
	m := NewMock(MockOptions{RingDelay: 5 * time.Millisecond})
	t.Cleanup(m.Close)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			to := "+9769911220" + string(rune('0'+i%10))
			res, err := m.Dial(ctx, mockReq(to, true))
			assert.NoError(t, err)
			assert.Equal(t, DefaultMockOutcome(to) == MockAnswered, res.Answered)
			_ = m.Hangup(ctx, "") // invalid, ignored
		}(i)
	}
	wg.Wait()
	assert.Len(t, m.Calls(), 20)
	rooms, _ := m.ListActiveRooms(ctx)
	assert.Len(t, rooms, 16, "8 of every 10 numbers answer")
}

func TestMockProvisioning(t *testing.T) {
	m := NewMock(MockOptions{})
	ctx := context.Background()
	n := testNumber()
	require.NoError(t, m.EnsureNumberProvisioned(ctx, n))
	assert.NotEmpty(t, n.InboundTrunkID)
	assert.NotEmpty(t, n.OutboundTrunkID)
	assert.NotEmpty(t, n.DispatchRuleID)
	ids := [3]string{n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID}
	require.NoError(t, m.EnsureNumberProvisioned(ctx, n))
	assert.Equal(t, ids, [3]string{n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID}, "stable")

	n.AllowInbound = false
	require.NoError(t, m.EnsureNumberProvisioned(ctx, n))
	assert.Empty(t, n.InboundTrunkID)
	assert.Empty(t, n.DispatchRuleID)

	require.NoError(t, m.DeprovisionNumber(ctx, n))
	assert.Empty(t, n.OutboundTrunkID)
	require.ErrorIs(t, m.EnsureNumberProvisioned(ctx, &domain.SIPNumber{}), domain.ErrInvalid)
}
