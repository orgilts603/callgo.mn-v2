package live

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// SimOptions tunes a Simulator. Zero values select the defaults.
type SimOptions struct {
	// Interval between new calls (default 12s). Jittered by ±25%.
	Interval time.Duration
	// MaxConcurrent caps simultaneous simulated calls (default 4).
	MaxConcurrent int
	// Seed makes the generated conversations reproducible (0 = time based).
	Seed int64
	// TimeScale multiplies every intra-call delay (ring time, turn time).
	// 1 (or 0) is real time; tests use a small value. Interval is unaffected.
	TimeScale float64
	// Logger receives diagnostics (default: disabled).
	Logger zerolog.Logger
}

// Simulator generates realistic fake calls through the repositories and the
// event bus, emitting the same event sequence as a real call:
// call.started, call.ringing, call.answered, agent.state / transcript.partial /
// transcript.final per turn, call.ended.
type Simulator struct {
	calls    domain.CallRepository
	contacts domain.ContactRepository
	bus      domain.EventBus
	orgID    uuid.UUID
	opts     SimOptions
	log      zerolog.Logger

	mu   sync.Mutex
	rng  *rand.Rand // master; only used under mu to seed per-call generators
	pool []domain.Contact
}

// NewSimulator builds a Simulator. contacts may be nil, in which case calls are
// created without a contact.
func NewSimulator(calls domain.CallRepository, contacts domain.ContactRepository, bus domain.EventBus, orgID uuid.UUID, opts SimOptions) *Simulator {
	if opts.Interval <= 0 {
		opts.Interval = 12 * time.Second
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 4
	}
	if opts.TimeScale <= 0 {
		opts.TimeScale = 1
	}
	seed := opts.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	return &Simulator{
		calls:    calls,
		contacts: contacts,
		bus:      bus,
		orgID:    orgID,
		opts:     opts,
		log:      opts.Logger.With().Str("component", "live.simulator").Logger(),
		rng:      rand.New(rand.NewSource(seed)),
	}
}

// Run starts a call immediately and then one every Interval (never exceeding
// MaxConcurrent in flight) until ctx is cancelled. Calls still in progress at
// cancellation are ended as failed and persisted. Run returns nil after all
// calls have finished.
func (s *Simulator) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	sem := make(chan struct{}, s.opts.MaxConcurrent)
	launch := func() {
		select {
		case sem <- struct{}{}:
		default:
			return // at capacity; skip this tick
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.SimulateCall(ctx); err != nil && ctx.Err() == nil {
				s.log.Error().Err(err).Msg("simulated call failed")
			}
		}()
	}

	launch()
	for {
		timer := time.NewTimer(s.nextInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			wg.Wait()
			return nil
		case <-timer.C:
			launch()
		}
	}
}

func (s *Simulator) nextInterval() time.Duration {
	s.mu.Lock()
	f := 0.75 + s.rng.Float64()*0.5
	s.mu.Unlock()
	return time.Duration(float64(s.opts.Interval) * f)
}

// callRand returns an independent generator for one call.
func (s *Simulator) callRand() *rand.Rand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return rand.New(rand.NewSource(s.rng.Int63()))
}

// SimulateCall runs one complete fake call synchronously and returns its
// final persisted state. If ctx is cancelled mid-call the call is closed as
// failed and ctx.Err() is returned along with the final call.
func (s *Simulator) SimulateCall(ctx context.Context) (*domain.Call, error) {
	r := s.callRand()
	sc := scenarios[r.Intn(len(scenarios))]
	// Direction is random; pick a scenario that matches it.
	dir := domain.DirectionInbound
	if r.Intn(2) == 0 {
		dir = domain.DirectionOutbound
	}
	for sc.direction != dir {
		sc = scenarios[r.Intn(len(scenarios))]
	}

	contact := s.pickContact(ctx, r)
	customerPhone := randomMobile(r)
	if contact != nil {
		customerPhone = contact.Phone
	}
	own := ownNumbers[r.Intn(len(ownNumbers))]
	now := time.Now().UTC()
	call := &domain.Call{
		ID:        uuid.New(),
		OrgID:     s.orgID,
		Direction: dir,
		Status:    domain.StatusRinging,
		RoomName:  "call-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		SIPCallID: "sim-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		StartedAt: now,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  map[string]any{"simulated": true},
	}
	if dir == domain.DirectionInbound {
		call.FromNumber, call.ToNumber = customerPhone, own
	} else {
		call.Status = domain.StatusQueued
		call.FromNumber, call.ToNumber = own, customerPhone
	}
	givenName := "Үйлчлүүлэгч"
	if contact != nil {
		call.ContactID = &contact.ID
		givenName = firstWord(contact.Name)
	}
	run := &callRun{s: s, r: r, call: call, sc: sc, name: givenName, model: llmModels[r.Intn(len(llmModels))]}
	err := run.execute(ctx)
	final := *call
	return &final, err
}

// pickContact reuses a known contact (~50 %) or creates a new one.
func (s *Simulator) pickContact(ctx context.Context, r *rand.Rand) *domain.Contact {
	if s.contacts == nil {
		return nil
	}
	s.mu.Lock()
	if len(s.pool) > 0 && r.Intn(2) == 0 {
		c := s.pool[r.Intn(len(s.pool))]
		s.mu.Unlock()
		return &c
	}
	s.mu.Unlock()

	now := time.Now().UTC()
	c := domain.Contact{
		ID:        uuid.New(),
		OrgID:     s.orgID,
		Phone:     randomMobile(r),
		Name:      familyNames[r.Intn(len(familyNames))] + " " + givenNames[r.Intn(len(givenNames))],
		Tags:      []string{"simulated"},
		Meta:      map[string]string{"source": "simulator"},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.contacts.UpsertContact(ctx, &c); err != nil {
		s.log.Warn().Err(err).Msg("upsert contact; continuing without contact")
		return nil
	}
	s.mu.Lock()
	if len(s.pool) < 24 {
		s.pool = append(s.pool, c)
	}
	s.mu.Unlock()
	return &c
}

// randomMobile returns a Mongolian mobile number such as +97699123456.
func randomMobile(r *rand.Rand) string {
	return fmt.Sprintf("+9769%07d", r.Intn(10_000_000))
}

func firstWord(name string) string {
	f := strings.Fields(name)
	if len(f) == 0 {
		return name
	}
	return f[len(f)-1] // Mongolian names: "<family> <given>"
}

// callRun is the state of a single simulated call.
type callRun struct {
	s     *Simulator
	r     *rand.Rand
	call  *domain.Call
	sc    scenario
	name  string
	model string
}

func (c *callRun) publish(ctx context.Context, t domain.EventType, payload any) {
	id := c.call.ID
	c.s.bus.Publish(ctx, domain.Event{
		ID:      uuid.NewString(),
		Type:    t,
		OrgID:   c.s.orgID,
		CallID:  &id,
		At:      time.Now().UTC(),
		Payload: payload,
	})
}

func (c *callRun) publishCall(ctx context.Context, t domain.EventType) {
	c.publish(ctx, t, map[string]any{"call": c.snapshot()})
}

// snapshot copies the call so later mutation cannot race with consumers.
func (c *callRun) snapshot() domain.Call {
	cp := *c.call
	if cp.AnsweredAt != nil {
		t := *cp.AnsweredAt
		cp.AnsweredAt = &t
	}
	if cp.EndedAt != nil {
		t := *cp.EndedAt
		cp.EndedAt = &t
	}
	return cp
}

func (c *callRun) persist(ctx context.Context) {
	c.call.UpdatedAt = time.Now().UTC()
	if err := c.s.calls.UpdateCall(ctx, c.call); err != nil {
		c.s.log.Warn().Err(err).Str("callId", c.call.ID.String()).Msg("update call")
	}
}

// sleep waits for d scaled by TimeScale; false means ctx was cancelled.
func (c *callRun) sleep(ctx context.Context, d time.Duration) bool {
	d = time.Duration(float64(d) * c.s.opts.TimeScale)
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (c *callRun) between(lo, hi time.Duration) time.Duration {
	return lo + time.Duration(c.r.Int63n(int64(hi-lo)+1))
}

func (c *callRun) execute(ctx context.Context) error {
	if err := c.s.calls.CreateCall(ctx, c.call); err != nil {
		return fmt.Errorf("simulator: create call: %w", err)
	}
	c.publishCall(ctx, domain.EventCallStarted)
	if !c.sleep(ctx, c.between(300*time.Millisecond, 800*time.Millisecond)) {
		return c.abort()
	}

	c.call.Status = domain.StatusRinging
	c.persist(ctx)
	c.publishCall(ctx, domain.EventCallRinging)
	if !c.sleep(ctx, c.between(1*time.Second, 3*time.Second)) {
		return c.abort()
	}

	// Only outbound calls can go unanswered; inbound is answered by the agent.
	if c.call.Direction == domain.DirectionOutbound {
		switch p := c.r.Intn(100); {
		case p < 18:
			c.finishUnanswered(ctx, domain.StatusNoAnswer, "no_answer")
			return nil
		case p < 28:
			c.finishUnanswered(ctx, domain.StatusBusy, "busy")
			return nil
		}
	}

	answered := time.Now().UTC()
	c.call.Status = domain.StatusActive
	c.call.AnsweredAt = &answered
	c.call.LLMModelUsed = c.model
	c.persist(ctx)
	c.publishCall(ctx, domain.EventCallAnswered)
	c.publishState(ctx, "initializing")
	if !c.sleep(ctx, c.between(200*time.Millisecond, 500*time.Millisecond)) {
		return c.abort()
	}
	c.publishState(ctx, "listening")

	for i, ln := range c.sc.lines {
		if !c.speak(ctx, i, ln, answered) {
			return c.abort()
		}
	}
	if !c.sleep(ctx, c.between(300*time.Millisecond, 900*time.Millisecond)) {
		return c.abort()
	}

	reason := "hangup_customer"
	switch p := c.r.Intn(100); {
	case p >= 92:
		reason = "transferred"
	case p >= 60:
		reason = "hangup_agent"
	}
	c.publishState(ctx, "idle")
	ended := time.Now().UTC()
	c.call.Status = domain.StatusCompleted
	c.call.EndedAt = &ended
	c.call.DurationSec = int(ended.Sub(answered).Seconds())
	c.call.EndReason = reason
	c.call.Summary = strings.ReplaceAll(c.sc.summary, "{name}", c.name)
	c.call.Intent = c.sc.intent
	c.call.Sentiment = c.sentiment()
	c.persist(ctx)
	c.publish(ctx, domain.EventCallEnded, map[string]any{
		"call":         c.snapshot(),
		"endReason":    reason,
		"summary":      c.call.Summary,
		"sentiment":    string(c.call.Sentiment),
		"intent":       c.call.Intent,
		"durationSec":  c.call.DurationSec,
		"llmModelUsed": c.model,
	})
	return nil
}

// sentiment mostly follows the scenario but occasionally drifts to neutral.
func (c *callRun) sentiment() domain.Sentiment {
	if c.sc.sentiment != domain.SentimentNeutral && c.r.Intn(10) == 0 {
		return domain.SentimentNeutral
	}
	return c.sc.sentiment
}

func (c *callRun) finishUnanswered(ctx context.Context, status domain.CallStatus, reason string) {
	ended := time.Now().UTC()
	c.call.Status = status
	c.call.EndedAt = &ended
	c.call.EndReason = reason
	c.call.DurationSec = int(ended.Sub(c.call.StartedAt).Seconds())
	c.persist(ctx)
	c.publish(ctx, domain.EventCallEnded, map[string]any{
		"call":        c.snapshot(),
		"endReason":   reason,
		"durationSec": c.call.DurationSec,
	})
}

// abort closes a call interrupted by shutdown. It uses a fresh context so the
// terminal state is still persisted and published.
func (c *callRun) abort() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ended := time.Now().UTC()
	c.call.Status = domain.StatusFailed
	c.call.EndedAt = &ended
	c.call.EndReason = "failed"
	c.call.Summary = "Симулятор зогссон тул дуудлага таслагдлаа."
	start := c.call.StartedAt
	if c.call.AnsweredAt != nil {
		start = *c.call.AnsweredAt
	}
	c.call.DurationSec = int(ended.Sub(start).Seconds())
	c.persist(ctx)
	c.publish(ctx, domain.EventCallEnded, map[string]any{
		"call":        c.snapshot(),
		"endReason":   "failed",
		"summary":     c.call.Summary,
		"durationSec": c.call.DurationSec,
	})
	return errors.New("simulator: call interrupted by shutdown")
}

func (c *callRun) publishState(ctx context.Context, state string) {
	c.publish(ctx, domain.EventAgentState, map[string]any{"state": state, "llmModel": c.model})
}

// speak plays one scripted utterance: state changes, partial transcripts and
// the persisted final turn. It returns false when ctx was cancelled.
func (c *callRun) speak(ctx context.Context, seq int, ln line, answeredAt time.Time) bool {
	text := strings.ReplaceAll(ln.text, "{name}", c.name)
	total := c.between(1*time.Second, 3*time.Second)
	startMs := int(time.Since(answeredAt).Milliseconds())

	const partials = 2
	step := total / (partials + 1)
	if ln.speaker == domain.SpeakerAgent {
		c.publishState(ctx, "thinking")
		if !c.sleep(ctx, step/2) {
			return false
		}
		c.publishState(ctx, "speaking")
	} else {
		c.publishState(ctx, "listening")
	}
	words := strings.Fields(text)
	for p := 1; p <= partials; p++ {
		if !c.sleep(ctx, step) {
			return false
		}
		n := len(words) * p / (partials + 1)
		if n < 1 {
			n = 1
		}
		c.publish(ctx, domain.EventTranscriptPartial, map[string]any{
			"speaker": string(ln.speaker),
			"text":    strings.Join(words[:n], " "),
			"startMs": startMs,
		})
	}
	if !c.sleep(ctx, step) {
		return false
	}

	turn := domain.TranscriptTurn{
		ID:         uuid.New(),
		CallID:     c.call.ID,
		Seq:        seq,
		Speaker:    ln.speaker,
		Text:       text,
		Confidence: 1,
		StartMs:    startMs,
		EndMs:      int(time.Since(answeredAt).Milliseconds()),
		IsFinal:    true,
		CreatedAt:  time.Now().UTC(),
	}
	if ln.speaker == domain.SpeakerCustomer {
		turn.Confidence = float32(0.82 + c.r.Float64()*0.17)
		turn.RawText = rawSTT(text)
	}
	if err := c.s.calls.AddTurn(ctx, &turn); err != nil {
		c.s.log.Warn().Err(err).Str("callId", c.call.ID.String()).Msg("add turn")
	}
	c.publish(ctx, domain.EventTranscriptFinal, map[string]any{"turn": turn})
	if ln.speaker == domain.SpeakerAgent {
		c.publishState(ctx, "listening")
	}
	return true
}

// rawSTT approximates the un-normalised recogniser output: lower-case,
// without punctuation.
func rawSTT(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}
