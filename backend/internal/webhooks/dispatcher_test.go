package webhooks_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/webhooks"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/webhooks/webhookstest"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type recBus struct {
	mu  sync.Mutex
	evs []domain.Event
}

func (b *recBus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	b.evs = append(b.evs, ev)
	b.mu.Unlock()
}
func (b *recBus) events() []domain.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.Event(nil), b.evs...)
}

type received struct {
	body []byte
	hdr  http.Header
}

type rig struct {
	repo  *webhookstest.MemRepo
	clock *fakeClock
	bus   *recBus
	d     *webhooks.Dispatcher
	org   uuid.UUID
}

func newRig(t *testing.T, cfg webhooks.Config) *rig {
	t.Helper()
	clock := newClock()
	repo := webhookstest.New()
	repo.Now = clock.Now
	cfg.Now = clock.Now
	bus := &recBus{}
	return &rig{repo: repo, clock: clock, bus: bus, org: uuid.New(),
		d: webhooks.New(repo, nil, cfg, bus, zerolog.Nop())}
}

func (r *rig) addHook(t *testing.T, url string, events ...string) *domain.Webhook {
	t.Helper()
	w := &domain.Webhook{ID: uuid.New(), OrgID: r.org, URL: url, Secret: "whsec_test", Events: events, Active: true}
	require.NoError(t, r.repo.CreateWebhook(context.Background(), w))
	return w
}

func (r *rig) event(typ domain.EventType) domain.Event {
	return domain.Event{ID: "ev-" + uuid.NewString(), Type: typ, OrgID: r.org, Payload: map[string]any{"hello": "world"}}
}

// runOnce starts Run, waits until cond holds, then stops it.
func runUntil(t *testing.T, d *webhooks.Dispatcher, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond)
	cancel()
	<-done
}

func TestSignVerify(t *testing.T) {
	sig := webhooks.Sign("s3cret", "1700000000", []byte(`{"a":1}`))
	require.Regexp(t, `^sha256=[0-9a-f]{64}$`, sig)
	require.True(t, webhooks.Verify("s3cret", "1700000000", []byte(`{"a":1}`), sig))
	require.False(t, webhooks.Verify("other", "1700000000", []byte(`{"a":1}`), sig))
	require.False(t, webhooks.Verify("s3cret", "1700000001", []byte(`{"a":1}`), sig))
	require.False(t, webhooks.Verify("s3cret", "1700000000", []byte(`{"a":2}`), sig))
	require.False(t, webhooks.Verify("s3cret", "1700000000", []byte(`{"a":1}`), sig[len("sha256="):]))
}

func TestValidateEvents(t *testing.T) {
	require.NoError(t, webhooks.ValidateEvents([]string{"*"}))
	require.NoError(t, webhooks.ValidateEvents([]string{"call.ended", "webhook.failed"}))
	require.Error(t, webhooks.ValidateEvents(nil))
	require.Error(t, webhooks.ValidateEvents([]string{"call.exploded"}))
	require.Error(t, webhooks.ValidateEvents([]string{"transcript.partial"}))
}

func TestNewSecret(t *testing.T) {
	s, hint, err := webhooks.NewSecret()
	require.NoError(t, err)
	require.Len(t, s, len("whsec_")+64)
	require.Equal(t, "whsec_…"+s[len(s)-4:], hint)
}

func TestEnqueueMatchesSubscriptions(t *testing.T) {
	r := newRig(t, webhooks.Config{})
	all := r.addHook(t, "https://a.example/hook", "*")
	ended := r.addHook(t, "https://b.example/hook", "call.ended")
	started := r.addHook(t, "https://c.example/hook", "call.started")
	inactive := r.addHook(t, "https://d.example/hook", "*")
	inactive.Active = false
	require.NoError(t, r.repo.UpdateWebhook(context.Background(), inactive))
	// other org must never receive our events
	other := &domain.Webhook{ID: uuid.New(), OrgID: uuid.New(), URL: "https://e.example", Secret: "x", Events: []string{"*"}, Active: true}
	require.NoError(t, r.repo.CreateWebhook(context.Background(), other))

	require.NoError(t, r.d.Enqueue(context.Background(), r.event(domain.EventCallEnded)))
	got := map[uuid.UUID]bool{}
	for _, dl := range r.repo.Deliveries() {
		got[dl.WebhookID] = true
		require.Equal(t, "pending", dl.Status)
		require.Equal(t, domain.EventCallEnded, dl.EventType)
		var ev domain.Event
		require.NoError(t, json.Unmarshal(dl.Payload, &ev))
		require.Equal(t, dl.EventID, ev.ID)
	}
	require.Equal(t, map[uuid.UUID]bool{all.ID: true, ended.ID: true}, got)
	require.NotContains(t, got, started.ID)

	// Non deliverable event types are dropped before any lookup.
	before := len(r.repo.Deliveries())
	require.NoError(t, r.d.Enqueue(context.Background(), r.event(domain.EventTranscriptPartial)))
	require.NoError(t, r.d.Enqueue(context.Background(), r.event(domain.EventAgentState)))
	require.Len(t, r.repo.Deliveries(), before)
}

func TestDeliverySignedAndDelivered(t *testing.T) {
	r := newRig(t, webhooks.Config{PollInterval: 5 * time.Millisecond})
	var mu sync.Mutex
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		mu.Lock()
		got = append(got, received{b, req.Header.Clone()})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	hook := r.addHook(t, srv.URL, "call.ended")

	ev := r.event(domain.EventCallEnded)
	require.NoError(t, r.d.Enqueue(context.Background(), ev))
	runUntil(t, r.d, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})
	require.Eventually(t, func() bool { return r.repo.Deliveries()[0].Status == "delivered" }, 2*time.Second, 5*time.Millisecond)

	rc := got[0]
	ts := rc.hdr.Get(webhooks.HeaderTimestamp)
	require.Equal(t, "1790762400", ts) // fake clock 2026-09-30T10:00:00Z
	require.Equal(t, "call.ended", rc.hdr.Get(webhooks.HeaderEvent))
	require.Equal(t, "application/json", rc.hdr.Get("Content-Type"))
	require.True(t, webhooks.Verify("whsec_test", ts, rc.body, rc.hdr.Get(webhooks.HeaderSignature)))
	dl := r.repo.Deliveries()[0]
	require.Equal(t, dl.ID.String(), rc.hdr.Get(webhooks.HeaderDelivery))
	var env domain.Event
	require.NoError(t, json.Unmarshal(rc.body, &env))
	require.Equal(t, ev.ID, env.ID)
	require.Equal(t, r.org, env.OrgID)

	require.Equal(t, 1, dl.Attempts)
	require.Equal(t, 204, dl.ResponseCode)
	require.Nil(t, dl.NextTryAt)
	w, err := r.repo.GetWebhook(context.Background(), hook.ID)
	require.NoError(t, err)
	require.Equal(t, 0, w.FailureCount)
	require.Equal(t, 204, w.LastStatus)
	require.NotNil(t, w.LastAt)
}

func TestRetryBackoffThenFailed(t *testing.T) {
	r := newRig(t, webhooks.Config{PollInterval: 5 * time.Millisecond})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	hook := r.addHook(t, srv.URL, "*")
	require.NoError(t, r.d.Enqueue(context.Background(), r.event(domain.EventCallEnded)))

	backoff := webhooks.DefaultBackoff
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.d.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitAttempts := func(n int) domain.WebhookDelivery {
		t.Helper()
		require.Eventually(t, func() bool {
			dl := r.repo.Deliveries()[0]
			return dl.Attempts == n && dl.Status != "" && (dl.Status == "failed" || dl.NextTryAt != nil && dl.NextTryAt.After(r.clock.Now()))
		}, 3*time.Second, 5*time.Millisecond, "attempt %d", n)
		return r.repo.Deliveries()[0]
	}

	dl := waitAttempts(1)
	require.Equal(t, "pending", dl.Status)
	require.Equal(t, 500, dl.ResponseCode)
	require.Contains(t, dl.LastError, "http 500: boom")
	require.WithinDuration(t, r.clock.Now().Add(backoff[0]), *dl.NextTryAt, time.Millisecond)

	// Not due yet: advancing less than the backoff does not trigger a retry.
	r.clock.Advance(backoff[0] - time.Second)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), hits.Load())

	for i := 1; i < len(backoff); i++ {
		r.clock.Advance(backoff[i-1]) // crosses the due time
		dl = waitAttempts(i + 1)
		require.Equal(t, "pending", dl.Status, "attempt %d", i+1)
		require.WithinDuration(t, r.clock.Now().Add(backoff[i]), *dl.NextTryAt, time.Millisecond)
	}
	r.clock.Advance(backoff[len(backoff)-1])
	dl = waitAttempts(len(backoff) + 1)
	require.Equal(t, "failed", dl.Status)
	require.Nil(t, dl.NextTryAt)
	require.Equal(t, int32(len(backoff)+1), hits.Load())

	w, _ := r.repo.GetWebhook(context.Background(), hook.ID)
	require.Equal(t, len(backoff)+1, w.FailureCount)
	require.True(t, w.Active)
}

func TestSuccessResetsFailureCount(t *testing.T) {
	r := newRig(t, webhooks.Config{PollInterval: 5 * time.Millisecond})
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	hook := r.addHook(t, srv.URL, "*")
	require.NoError(t, r.d.Enqueue(context.Background(), r.event(domain.EventCallEnded)))
	runUntil(t, r.d, func() bool { return r.repo.Deliveries()[0].Attempts == 1 })
	w, _ := r.repo.GetWebhook(context.Background(), hook.ID)
	require.Equal(t, 1, w.FailureCount)
	require.Equal(t, 502, w.LastStatus)

	fail.Store(false)
	r.clock.Advance(2 * time.Minute)
	runUntil(t, r.d, func() bool { return r.repo.Deliveries()[0].Status == "delivered" })
	w, _ = r.repo.GetWebhook(context.Background(), hook.ID)
	require.Equal(t, 0, w.FailureCount)
	require.Equal(t, 200, w.LastStatus)
}

func TestAutoDisableAfterConsecutiveFailures(t *testing.T) {
	r := newRig(t, webhooks.Config{PollInterval: 5 * time.Millisecond, MaxFailuresToDisable: 3, Backoff: []time.Duration{time.Minute}, MaxAttempts: 10})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	hook := r.addHook(t, srv.URL, "*")
	require.NoError(t, r.d.Enqueue(context.Background(), r.event(domain.EventCallEnded)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.d.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	for i := 1; i <= 3; i++ {
		i := i
		require.Eventually(t, func() bool { return int(hits.Load()) == i && r.repo.Deliveries()[0].Attempts == i }, 3*time.Second, 5*time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		r.clock.Advance(time.Minute + time.Second)
	}
	// After the 3rd failure the webhook is disabled; the 4th claim marks the
	// pending delivery failed without calling the endpoint.
	require.Eventually(t, func() bool { return r.repo.Deliveries()[0].Status == "failed" }, 3*time.Second, 5*time.Millisecond)
	require.Equal(t, int32(3), hits.Load())
	require.Equal(t, "webhook is disabled", r.repo.Deliveries()[0].LastError)

	w, _ := r.repo.GetWebhook(context.Background(), hook.ID)
	require.False(t, w.Active)
	require.Equal(t, 3, w.FailureCount)

	evs := r.bus.events()
	require.Len(t, evs, 1)
	require.Equal(t, webhooks.EventWebhookFailed, evs[0].Type)
	require.Equal(t, r.org, evs[0].OrgID)
	p := evs[0].Payload.(map[string]any)
	require.Equal(t, hook.ID, p["webhookId"])
	require.Equal(t, srv.URL, p["url"])

	// A disabled webhook no longer receives new events.
	before := len(r.repo.Deliveries())
	require.NoError(t, r.d.Enqueue(context.Background(), r.event(domain.EventCallEnded)))
	require.Len(t, r.repo.Deliveries(), before)
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	r := newRig(t, webhooks.Config{})
	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHit.Store(true) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target.URL, http.StatusFound)
	}))
	defer srv.Close()
	hook := r.addHook(t, srv.URL, "*")
	dl, err := r.d.Test(context.Background(), hook.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", dl.Status)
	require.Equal(t, 302, dl.ResponseCode)
	require.False(t, targetHit.Load())
}

func TestTimeout(t *testing.T) {
	r := newRig(t, webhooks.Config{Timeout: 50 * time.Millisecond})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	hook := r.addHook(t, srv.URL, "*")
	dl, err := r.d.Test(context.Background(), hook.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", dl.Status)
	require.Equal(t, 0, dl.ResponseCode)
	require.Contains(t, dl.LastError, "request failed")
}

func TestTestSendsSystemEventEvenWhenInactive(t *testing.T) {
	r := newRig(t, webhooks.Config{})
	var got received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got.body, _ = io.ReadAll(req.Body)
		got.hdr = req.Header.Clone()
	}))
	defer srv.Close()
	hook := r.addHook(t, srv.URL, "call.ended")
	hook.Active = false
	require.NoError(t, r.repo.UpdateWebhook(context.Background(), hook))

	dl, err := r.d.Test(context.Background(), hook.ID)
	require.NoError(t, err)
	require.Equal(t, "delivered", dl.Status)
	require.Equal(t, domain.EventSystem, dl.EventType)
	require.Equal(t, "system", got.hdr.Get(webhooks.HeaderEvent))
	require.True(t, webhooks.Verify("whsec_test", got.hdr.Get(webhooks.HeaderTimestamp), got.body, got.hdr.Get(webhooks.HeaderSignature)))
	stored := r.repo.Deliveries()
	require.Len(t, stored, 1)
	require.Equal(t, "delivered", stored[0].Status)

	_, err = r.d.Test(context.Background(), uuid.New())
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestEnqueueFor(t *testing.T) {
	r := newRig(t, webhooks.Config{})
	// Subscribed to a different event: EnqueueFor ignores subscriptions.
	hook := r.addHook(t, "https://x.example", "call.started")
	ev := r.event(domain.EventCallEnded)
	require.NoError(t, r.d.EnqueueFor(context.Background(), hook.ID, ev))
	dls := r.repo.Deliveries()
	require.Len(t, dls, 1)
	require.Equal(t, hook.ID, dls[0].WebhookID)

	// Wrong org.
	ev2 := ev
	ev2.OrgID = uuid.New()
	require.ErrorIs(t, r.d.EnqueueFor(context.Background(), hook.ID, ev2), domain.ErrNotFound)
	// Unknown webhook.
	require.ErrorIs(t, r.d.EnqueueFor(context.Background(), uuid.New(), ev), domain.ErrNotFound)
	// Inactive.
	hook.Active = false
	require.NoError(t, r.repo.UpdateWebhook(context.Background(), hook))
	require.ErrorIs(t, r.d.EnqueueFor(context.Background(), hook.ID, ev), webhooks.ErrWebhookInactive)
	require.Len(t, r.repo.Deliveries(), 1)
}

func TestRetryManual(t *testing.T) {
	r := newRig(t, webhooks.Config{PollInterval: 5 * time.Millisecond})
	var ok atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !ok.Load() {
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	hook := r.addHook(t, srv.URL, "*")
	dl, err := r.d.Test(context.Background(), hook.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", dl.Status)

	ok.Store(true)
	// Another org cannot retry it.
	_, err = r.d.Retry(context.Background(), uuid.New(), dl.ID)
	require.ErrorIs(t, err, domain.ErrNotFound)

	got, err := r.d.Retry(context.Background(), r.org, dl.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
	require.Equal(t, 0, got.Attempts)
	runUntil(t, r.d, func() bool { return r.repo.Deliveries()[0].Status == "delivered" })

	_, err = r.d.Retry(context.Background(), r.org, uuid.New())
	require.ErrorIs(t, err, domain.ErrNotFound)

	hook.Active = false
	require.NoError(t, r.repo.UpdateWebhook(context.Background(), hook))
	_, err = r.d.Retry(context.Background(), r.org, dl.ID)
	require.ErrorIs(t, err, webhooks.ErrWebhookInactive)
}

func TestTeeBus(t *testing.T) {
	r := newRig(t, webhooks.Config{})
	r.addHook(t, "https://x.example", "call.ended")
	inner := &recBus{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tee := webhooks.NewTeeBus(ctx, inner, r.d, zerolog.Nop())

	tee.Publish(ctx, domain.Event{Type: domain.EventTranscriptPartial, OrgID: r.org})
	tee.Publish(ctx, domain.Event{Type: domain.EventCallEnded, OrgID: r.org})
	got := inner.events()
	require.Len(t, got, 2)
	require.NotEmpty(t, got[1].ID)
	require.False(t, got[1].At.IsZero())
	require.Eventually(t, func() bool { return len(r.repo.Deliveries()) == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, got[1].ID, r.repo.Deliveries()[0].EventID)
}
