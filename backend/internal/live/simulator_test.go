package live

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// fakeCalls is a minimal in-memory domain.CallRepository.
type fakeCalls struct {
	mu    sync.Mutex
	calls map[uuid.UUID]domain.Call
	turns map[uuid.UUID][]domain.TranscriptTurn
}

func newFakeCalls() *fakeCalls {
	return &fakeCalls{calls: map[uuid.UUID]domain.Call{}, turns: map[uuid.UUID][]domain.TranscriptTurn{}}
}

func (f *fakeCalls) CreateCall(_ context.Context, c *domain.Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[c.ID] = *c
	return nil
}
func (f *fakeCalls) UpdateCall(_ context.Context, c *domain.Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.calls[c.ID]; !ok {
		return domain.ErrNotFound
	}
	f.calls[c.ID] = *c
	return nil
}
func (f *fakeCalls) GetCall(_ context.Context, id uuid.UUID) (*domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}
func (f *fakeCalls) AddTurn(_ context.Context, t *domain.TranscriptTurn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.turns[t.CallID] = append(f.turns[t.CallID], *t)
	return nil
}
func (f *fakeCalls) ListTurns(_ context.Context, id uuid.UUID) ([]domain.TranscriptTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.TranscriptTurn(nil), f.turns[id]...), nil
}
func (f *fakeCalls) all() []domain.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Call, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c)
	}
	return out
}

// Unused parts of the port.
func (f *fakeCalls) GetCallByRoom(context.Context, string) (*domain.Call, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCalls) ListCalls(context.Context, domain.CallFilter) ([]domain.Call, int, error) {
	return nil, 0, nil
}
func (f *fakeCalls) ListActiveCalls(context.Context, uuid.UUID) ([]domain.Call, error) {
	return nil, nil
}
func (f *fakeCalls) UpdateTurnText(context.Context, uuid.UUID, string) error { return nil }
func (f *fakeCalls) GetTurn(context.Context, uuid.UUID) (*domain.TranscriptTurn, error) {
	return nil, domain.ErrNotFound
}
func (f *fakeCalls) Stats(context.Context, uuid.UUID) (domain.CallStats, error) {
	return domain.CallStats{}, nil
}
func (f *fakeCalls) DailySeries(context.Context, uuid.UUID, int) ([]domain.DailyCallCount, error) {
	return nil, nil
}

// fakeContacts is a minimal in-memory domain.ContactRepository.
type fakeContacts struct {
	mu sync.Mutex
	m  map[uuid.UUID]domain.Contact
}

func newFakeContacts() *fakeContacts { return &fakeContacts{m: map[uuid.UUID]domain.Contact{}} }

func (f *fakeContacts) UpsertContact(_ context.Context, c *domain.Contact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[c.ID] = *c
	return nil
}
func (f *fakeContacts) GetContact(_ context.Context, id uuid.UUID) (*domain.Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.m[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}
func (f *fakeContacts) GetContactByPhone(_ context.Context, org uuid.UUID, phone string) (*domain.Contact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.m {
		if c.OrgID == org && c.Phone == phone {
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}
func (f *fakeContacts) ListContacts(context.Context, uuid.UUID, string, int, int) ([]domain.Contact, int, error) {
	return nil, 0, nil
}
func (f *fakeContacts) DeleteContact(context.Context, uuid.UUID) error { return nil }

// recorder is a domain.EventBus that stores every event.
type recorder struct {
	mu  sync.Mutex
	evs []domain.Event
}

func (r *recorder) Publish(_ context.Context, ev domain.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
}

func (r *recorder) byCall() map[uuid.UUID][]domain.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[uuid.UUID][]domain.Event{}
	for _, e := range r.evs {
		if e.CallID != nil {
			out[*e.CallID] = append(out[*e.CallID], e)
		}
	}
	return out
}

var mongolianPhone = regexp.MustCompile(`^\+9769\d{7}$`)

func TestSimulateCallProducesValidSequence(t *testing.T) {
	org := uuid.New()
	calls, contacts, bus := newFakeCalls(), newFakeContacts(), &recorder{}
	sim := NewSimulator(calls, contacts, bus, org, SimOptions{Seed: 42, TimeScale: 0.0005})

	outcomes := map[domain.CallStatus]int{}
	dirs := map[domain.CallDirection]int{}
	for i := 0; i < 60; i++ {
		c, err := sim.SimulateCall(context.Background())
		require.NoError(t, err)
		outcomes[c.Status]++
		dirs[c.Direction]++
	}
	assert.Positive(t, outcomes[domain.StatusCompleted])
	assert.Positive(t, outcomes[domain.StatusNoAnswer]+outcomes[domain.StatusBusy], "some outbound calls go unanswered")
	assert.Positive(t, dirs[domain.DirectionInbound])
	assert.Positive(t, dirs[domain.DirectionOutbound])
	assert.Zero(t, outcomes[domain.StatusFailed])

	perCall := bus.byCall()
	require.Len(t, perCall, 60)
	for id, evs := range perCall {
		persisted, err := calls.GetCall(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, org, persisted.OrgID)
		assert.True(t, persisted.Status.IsTerminal())
		assert.Regexp(t, mongolianPhone, phoneOfCustomer(*persisted))
		require.NotNil(t, persisted.EndedAt)

		types := make([]domain.EventType, len(evs))
		for i, e := range evs {
			types[i] = e.Type
			assert.Equal(t, org, e.OrgID)
			assert.NotEmpty(t, e.ID)
			assert.False(t, e.At.IsZero())
		}
		require.Equal(t, domain.EventCallStarted, types[0])
		require.Equal(t, domain.EventCallRinging, types[1])
		last := evs[len(evs)-1]
		require.Equal(t, domain.EventCallEnded, last.Type)
		payload := last.Payload.(map[string]any)
		assert.Equal(t, persisted.EndReason, payload["endReason"])

		if persisted.Status != domain.StatusCompleted {
			// Unanswered: nothing between ringing and ended.
			assert.Len(t, evs, 3)
			assert.Contains(t, []string{"no_answer", "busy"}, persisted.EndReason)
			assert.Equal(t, domain.DirectionOutbound, persisted.Direction)
			turns, _ := calls.ListTurns(context.Background(), id)
			assert.Empty(t, turns)
			continue
		}

		// Answered call.
		require.Equal(t, domain.EventCallAnswered, types[2])
		assert.NotNil(t, persisted.AnsweredAt)
		assert.NotEmpty(t, persisted.Summary)
		assert.NotEmpty(t, persisted.Intent)
		assert.Contains(t, []domain.Sentiment{domain.SentimentPositive, domain.SentimentNeutral, domain.SentimentNegative}, persisted.Sentiment)
		assert.NotEmpty(t, persisted.LLMModelUsed)
		assert.Contains(t, []string{"hangup_customer", "hangup_agent", "transferred"}, persisted.EndReason)
		for _, k := range []string{"summary", "sentiment", "intent", "durationSec", "llmModelUsed", "call"} {
			assert.Contains(t, payload, k)
		}

		turns, err := calls.ListTurns(context.Background(), id)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(turns), 6)
		assert.LessOrEqual(t, len(turns), 10)

		var finals []domain.TranscriptTurn
		partials := 0
		for i, e := range evs {
			switch e.Type {
			case domain.EventTranscriptFinal:
				finals = append(finals, e.Payload.(map[string]any)["turn"].(domain.TranscriptTurn))
			case domain.EventTranscriptPartial:
				partials++
				p := e.Payload.(map[string]any)
				assert.Contains(t, []string{"customer", "agent"}, p["speaker"])
				assert.NotEmpty(t, p["text"])
			case domain.EventCallStarted, domain.EventCallRinging, domain.EventCallAnswered:
				assert.Less(t, i, 3)
			}
		}
		require.Len(t, finals, len(turns), "every persisted turn is broadcast")
		assert.Equal(t, 2*len(turns), partials)
		for i, tr := range turns {
			assert.Equal(t, i, tr.Seq)
			assert.Equal(t, id, tr.CallID)
			assert.True(t, tr.IsFinal)
			assert.NotEmpty(t, tr.Text)
			assert.NotContains(t, tr.Text, "{name}")
			assert.Equal(t, finals[i].ID, tr.ID)
			if i > 0 {
				assert.Equal(t, turns[i-1].Speaker != tr.Speaker, true, "speakers alternate")
				assert.GreaterOrEqual(t, tr.StartMs, turns[i-1].StartMs)
			}
		}
		// agent.state events appear between answered and ended, listening/thinking/speaking present.
		states := map[string]bool{}
		for _, e := range evs {
			if e.Type == domain.EventAgentState {
				states[e.Payload.(map[string]any)["state"].(string)] = true
			}
		}
		for _, s := range []string{"initializing", "listening", "thinking", "speaking", "idle"} {
			assert.True(t, states[s], "missing agent.state %s", s)
		}
	}

	// Contacts were persisted and linked to calls; phones are Mongolian mobiles.
	assert.NotEmpty(t, contacts.m)
	for _, c := range contacts.m {
		assert.Regexp(t, mongolianPhone, c.Phone)
		assert.NotEmpty(t, c.Name)
		assert.Equal(t, org, c.OrgID)
	}
	for _, c := range calls.all() {
		require.NotNil(t, c.ContactID)
		_, err := contacts.GetContact(context.Background(), *c.ContactID)
		assert.NoError(t, err)
	}
}

// phoneOfCustomer returns the customer side of the call.
func phoneOfCustomer(c domain.Call) string {
	if c.Direction == domain.DirectionInbound {
		return c.FromNumber
	}
	return c.ToNumber
}

func TestSimulatorDialogueIsMongolian(t *testing.T) {
	calls, bus := newFakeCalls(), &recorder{}
	sim := NewSimulator(calls, nil, bus, uuid.New(), SimOptions{Seed: 7, TimeScale: 0.0005})
	for i := 0; i < 20; i++ {
		_, err := sim.SimulateCall(context.Background())
		require.NoError(t, err)
	}
	cyr := regexp.MustCompile(`[А-Яа-яӨөҮүЁё]`)
	seen := 0
	for _, turns := range calls.turns {
		for _, tr := range turns {
			seen++
			assert.Regexp(t, cyr, tr.Text)
			if tr.Speaker == domain.SpeakerCustomer {
				assert.Equal(t, strings.ToLower(tr.RawText), tr.RawText)
				assert.NotContains(t, tr.RawText, ",")
				assert.Greater(t, tr.Confidence, float32(0.8))
			}
		}
	}
	assert.Positive(t, seen)
	for _, c := range calls.all() {
		assert.Nil(t, c.ContactID, "no contact repository, no contact")
	}
}

func TestSimulatorIsDeterministicForSeed(t *testing.T) {
	summarize := func() []string {
		calls := newFakeCalls()
		sim := NewSimulator(calls, nil, &recorder{}, uuid.New(), SimOptions{Seed: 99, TimeScale: 0.0005})
		var out []string
		for i := 0; i < 10; i++ {
			c, err := sim.SimulateCall(context.Background())
			require.NoError(t, err)
			out = append(out, string(c.Direction)+"|"+c.Intent+"|"+string(c.Status)+"|"+phoneOfCustomer(*c))
		}
		return out
	}
	assert.Equal(t, summarize(), summarize())
}

func TestSimulatorRunLaunchesCallsAndStopsCleanly(t *testing.T) {
	org := uuid.New()
	calls, contacts := newFakeCalls(), newFakeContacts()
	hub := newTestHub(t, HubOptions{SendBuffer: 4096})
	events, cancelSub := hub.Subscribe(org)
	defer cancelSub()

	sim := NewSimulator(calls, contacts, hub, org, SimOptions{
		Interval: 5 * time.Millisecond, MaxConcurrent: 3, Seed: 3, TimeScale: 0.002,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sim.Run(ctx) }()

	ended, concurrent, maxConcurrent := 0, map[uuid.UUID]bool{}, 0
	deadline := time.After(20 * time.Second)
loop:
	for ended < 8 {
		select {
		case ev := <-events:
			switch ev.Type {
			case domain.EventCallStarted:
				concurrent[*ev.CallID] = true
				maxConcurrent = max(maxConcurrent, len(concurrent))
			case domain.EventCallEnded:
				delete(concurrent, *ev.CallID)
				ended++
			}
		case <-deadline:
			break loop
		}
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	require.GreaterOrEqual(t, ended, 8)
	assert.LessOrEqual(t, maxConcurrent, 3)

	// Every call — including any interrupted by the cancel — is terminal.
	all := calls.all()
	require.NotEmpty(t, all)
	for _, c := range all {
		assert.True(t, c.Status.IsTerminal(), "call %s left in status %s", c.ID, c.Status)
	}
	// Hub recorded the run.
	assert.NotEmpty(t, hub.Recent(org, 1000))
	ids := make([]string, 0)
	for _, e := range hub.Recent(org, 1000) {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	for i := 1; i < len(ids); i++ {
		assert.NotEqual(t, ids[i-1], ids[i], "event ids are unique")
	}
}

func TestSimulateCallCancelledMidCallIsClosedAsFailed(t *testing.T) {
	calls, bus := newFakeCalls(), &recorder{}
	sim := NewSimulator(calls, nil, bus, uuid.New(), SimOptions{Seed: 5, TimeScale: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	c, err := sim.SimulateCall(ctx)
	require.Error(t, err)
	assert.Equal(t, domain.StatusFailed, c.Status)
	persisted, err := calls.GetCall(context.Background(), c.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusFailed, persisted.Status)
	evs := bus.byCall()[c.ID]
	assert.Equal(t, domain.EventCallEnded, evs[len(evs)-1].Type)
}
