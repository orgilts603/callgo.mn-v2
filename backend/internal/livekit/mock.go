package livekit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// MockOutcome is how a simulated outbound call ends up.
type MockOutcome string

const (
	MockAnswered MockOutcome = "answered"
	MockBusy     MockOutcome = "busy"
	MockNoAnswer MockOutcome = "no_answer"
	MockFailed   MockOutcome = "failed"
)

// Events passed to MockOptions.OnEvent. "ringing" is always first; then
// either "answered" (followed later by "ended") or one terminal failure event
// ("busy", "no_answer", "failed"). A call hung up while ringing emits "ended".
const (
	MockEventRinging  = "ringing"
	MockEventAnswered = "answered"
	MockEventEnded    = "ended"
	MockEventBusy     = "busy"
	MockEventNoAnswer = "no_answer"
	MockEventFailed   = "failed"
)

// DefaultMockRingDelay is used when MockOptions.RingDelay is zero.
const DefaultMockRingDelay = 2 * time.Second

// MockOptions configures the Mock telephony.
type MockOptions struct {
	// RingDelay is how long a simulated call rings before its outcome
	// (default DefaultMockRingDelay).
	RingDelay time.Duration
	// CallDuration ends an answered call automatically after this long.
	// Zero keeps it up until Hangup (or Close).
	CallDuration time.Duration
	// Outcome decides the outcome per destination number. Default
	// (DefaultMockOutcome): numbers ending in 0 → no_answer, 1 → busy,
	// anything else → answered.
	Outcome func(to string) MockOutcome
	// OnEvent is called for every simulated state change, from the call's
	// own goroutine and in order per call. It may call back into the Mock
	// (e.g. Hangup). Use it to feed the live hub in dev.
	OnEvent func(roomName string, evt string)
}

// DefaultMockOutcome is the deterministic outcome rule of the Mock.
func DefaultMockOutcome(to string) MockOutcome {
	to = strings.TrimSpace(to)
	if to == "" {
		return MockFailed
	}
	switch to[len(to)-1] {
	case '0':
		return MockNoAnswer
	case '1':
		return MockBusy
	}
	return MockAnswered
}

// MockCall is the record of one simulated call.
type MockCall struct {
	Request       domain.OutboundCallRequest
	RoomName      string
	ParticipantID string
	SIPCallID     string
	// State is the last event of the call (see MockEvent* constants).
	State         string
	Outcome       MockOutcome
	TransferredTo string
	StartedAt     time.Time
	EndedAt       time.Time
}

type mockCall struct {
	MockCall
	cancel  context.CancelFunc
	ctx     context.Context
	decided chan domain.OutboundCallResult // buffered(1): ring outcome
}

// Mock is an in-memory domain.Telephony for development without a SIP trunk.
// It is safe for concurrent use. Call Close on shutdown to stop simulations.
type Mock struct {
	opts MockOptions

	mu     sync.Mutex
	calls  []*mockCall
	active map[string]*mockCall
	closed bool

	wg sync.WaitGroup
}

var _ domain.Telephony = (*Mock)(nil)

// NewMock returns a Mock telephony adapter.
func NewMock(opts MockOptions) *Mock {
	if opts.RingDelay <= 0 {
		opts.RingDelay = DefaultMockRingDelay
	}
	if opts.Outcome == nil {
		opts.Outcome = DefaultMockOutcome
	}
	return &Mock{opts: opts, active: map[string]*mockCall{}}
}

// EnsureNumberProvisioned fills deterministic fake LiveKit IDs, following the
// same AllowInbound / AllowOutbound rules as Client.
func (m *Mock) EnsureNumberProvisioned(_ context.Context, n *domain.SIPNumber) error {
	if err := validateNumber(n); err != nil {
		return err
	}
	suffix := strings.ReplaceAll(n.ID.String(), "-", "")[:12]
	if n.AllowInbound {
		if n.InboundTrunkID == "" {
			n.InboundTrunkID = "ST_mockin_" + suffix
		}
		if n.DispatchRuleID == "" {
			n.DispatchRuleID = "SDR_mock_" + suffix
		}
	} else {
		n.InboundTrunkID, n.DispatchRuleID = "", ""
	}
	if n.AllowOutbound {
		if n.OutboundTrunkID == "" {
			n.OutboundTrunkID = "ST_mockout_" + suffix
		}
	} else {
		n.OutboundTrunkID = ""
	}
	return nil
}

// DeprovisionNumber clears the fake IDs.
func (m *Mock) DeprovisionNumber(_ context.Context, n *domain.SIPNumber) error {
	if err := validateNumber(n); err != nil {
		return err
	}
	n.InboundTrunkID, n.OutboundTrunkID, n.DispatchRuleID = "", "", ""
	return nil
}

// Dial simulates an outbound call: it rings for RingDelay and then resolves
// per Outcome. With WaitUntilAnswered it blocks until then and returns the
// outcome like Client.Dial does (nil error, result.Error "<status>: ...");
// otherwise it returns immediately and the outcome is only reported via
// OnEvent.
func (m *Mock) Dial(ctx context.Context, req domain.OutboundCallRequest) (domain.OutboundCallResult, error) {
	if strings.TrimSpace(req.ToNumber) == "" {
		err := fmt.Errorf("%w: missing destination number", domain.ErrInvalid)
		return failed(err), err
	}
	room := req.RoomName
	if room == "" {
		room = RoomNameForCall(req.CallID)
		req.RoomName = room
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		err := errors.New("livekit mock: closed")
		return failed(err), err
	}
	if _, busy := m.active[room]; busy {
		m.mu.Unlock()
		err := fmt.Errorf("livekit mock: room %s already has a call: %w", room, domain.ErrConflict)
		return failed(err), err
	}
	cctx, cancel := context.WithCancel(context.Background())
	id := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	call := &mockCall{
		MockCall: MockCall{
			Request:       req,
			RoomName:      room,
			ParticipantID: "PA_mock" + id,
			SIPCallID:     "SCL_mock" + id,
			State:         MockEventRinging,
			StartedAt:     time.Now(),
		},
		ctx:     cctx,
		cancel:  cancel,
		decided: make(chan domain.OutboundCallResult, 1),
	}
	m.calls = append(m.calls, call)
	m.active[room] = call
	m.wg.Add(1)
	m.mu.Unlock()

	go m.run(call)

	if !req.WaitUntilAnswered {
		return domain.OutboundCallResult{ParticipantID: call.ParticipantID, SIPCallID: call.SIPCallID}, nil
	}
	select {
	case res := <-call.decided:
		return res, nil
	case <-ctx.Done():
		m.stop(call)
		err := fmt.Errorf("livekit mock: dial %s: %w", req.ToNumber, ctx.Err())
		return failed(err), err
	}
}

// run drives one simulated call. All OnEvent calls of the call happen here.
func (m *Mock) run(c *mockCall) {
	defer m.wg.Done()
	defer m.stop(c)

	m.emit(c, MockEventRinging)
	ring := time.NewTimer(m.opts.RingDelay)
	select {
	case <-c.ctx.Done():
		ring.Stop()
		m.finish(c, MockEventEnded)
		c.decided <- domain.OutboundCallResult{Error: string(domain.StatusFailed) + ": hung up while ringing"}
		m.emit(c, MockEventEnded)
		return
	case <-ring.C:
	}

	outcome := m.opts.Outcome(c.Request.ToNumber)
	m.mu.Lock()
	c.Outcome = outcome
	m.mu.Unlock()

	if outcome != MockAnswered {
		evt, status := MockEventFailed, domain.StatusFailed
		switch outcome {
		case MockBusy:
			evt, status = MockEventBusy, domain.StatusBusy
		case MockNoAnswer:
			evt, status = MockEventNoAnswer, domain.StatusNoAnswer
		}
		m.finish(c, evt)
		m.emit(c, evt)
		c.decided <- domain.OutboundCallResult{Error: string(status) + ": simulated"}
		return
	}

	m.setState(c, MockEventAnswered)
	m.emit(c, MockEventAnswered)
	c.decided <- domain.OutboundCallResult{ParticipantID: c.ParticipantID, SIPCallID: c.SIPCallID, Answered: true}

	var limit <-chan time.Time
	if m.opts.CallDuration > 0 {
		t := time.NewTimer(m.opts.CallDuration)
		defer t.Stop()
		limit = t.C
	}
	select {
	case <-c.ctx.Done():
	case <-limit:
	}
	m.finish(c, MockEventEnded)
	m.emit(c, MockEventEnded)
}

func (m *Mock) emit(c *mockCall, evt string) {
	if m.opts.OnEvent != nil {
		m.opts.OnEvent(c.RoomName, evt)
	}
}

func (m *Mock) setState(c *mockCall, state string) {
	m.mu.Lock()
	c.State = state
	m.mu.Unlock()
}

func (m *Mock) finish(c *mockCall, state string) {
	m.mu.Lock()
	c.State = state
	c.EndedAt = time.Now()
	m.mu.Unlock()
}

// stop cancels c and removes it from the active set (if still registered).
func (m *Mock) stop(c *mockCall) {
	m.mu.Lock()
	if m.active[c.RoomName] == c {
		delete(m.active, c.RoomName)
	}
	m.mu.Unlock()
	c.cancel()
}

// Hangup ends the simulated call in roomName. The "ended" event is emitted
// asynchronously by the call's goroutine. Unknown rooms are not an error.
func (m *Mock) Hangup(_ context.Context, roomName string) error {
	if roomName == "" {
		return fmt.Errorf("%w: missing room name", domain.ErrInvalid)
	}
	m.mu.Lock()
	c := m.active[roomName]
	m.mu.Unlock()
	if c != nil {
		m.stop(c)
	}
	return nil
}

// TransferCall records the transfer target of an active call.
func (m *Mock) TransferCall(_ context.Context, roomName, _ string, toNumber string) error {
	if strings.TrimSpace(toNumber) == "" {
		return fmt.Errorf("%w: missing transfer number", domain.ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.active[roomName]
	if c == nil {
		return fmt.Errorf("livekit mock: room %s: %w", roomName, domain.ErrNotFound)
	}
	c.TransferredTo = normalizeTransferTarget(toNumber)
	return nil
}

// ListActiveRooms lists rooms of calls that are ringing or answered.
func (m *Mock) ListActiveRooms(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.active))
	for _, c := range m.calls { // stable (dial) order
		if m.active[c.RoomName] == c {
			names = append(names, c.RoomName)
		}
	}
	return names, nil
}

// Calls returns a snapshot of every call dialled so far, in dial order.
func (m *Mock) Calls() []MockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MockCall, len(m.calls))
	for i, c := range m.calls {
		out[i] = c.MockCall
	}
	return out
}

// Close ends every simulated call and waits for their goroutines. Further
// Dial calls fail.
func (m *Mock) Close() {
	m.mu.Lock()
	m.closed = true
	active := make([]*mockCall, 0, len(m.active))
	for _, c := range m.active {
		active = append(active, c)
	}
	m.mu.Unlock()
	for _, c := range active {
		m.stop(c)
	}
	m.wg.Wait()
}
