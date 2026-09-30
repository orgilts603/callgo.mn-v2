package recording

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	lkproto "github.com/livekit/protocol/livekit"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/livekit"
)

// --- fakes -------------------------------------------------------------------

type fakeCalls struct {
	mu    sync.Mutex
	calls map[uuid.UUID]*domain.Call
	sets  int
}

func newFakeCalls(cs ...*domain.Call) *fakeCalls {
	f := &fakeCalls{calls: map[uuid.UUID]*domain.Call{}}
	for _, c := range cs {
		cp := *c
		f.calls[c.ID] = &cp
	}
	return f
}

func (f *fakeCalls) GetCall(_ context.Context, id uuid.UUID) (*domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (f *fakeCalls) GetCallByRoom(_ context.Context, room string) (*domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.RoomName == room {
			cp := *c
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCalls) SetCallRecording(_ context.Context, id uuid.UUID, rec *domain.RecordingInfo) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.calls[id]
	if !ok {
		return domain.ErrNotFound
	}
	f.sets++
	if rec == nil {
		c.Recording = nil
	} else {
		r := *rec
		c.Recording = &r
	}
	if rec != nil && rec.Status == StatusReady {
		c.RecordingURL = URLPath(id)
	} else {
		c.RecordingURL = ""
	}
	return nil
}

func (f *fakeCalls) ListCallsForRetention(_ context.Context, before time.Time, limit int) ([]domain.Call, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Call
	for _, c := range f.calls {
		if c.Recording == nil || c.Recording.ObjectKey == "" {
			continue
		}
		if c.Recording.Status != StatusReady && c.Recording.Status != StatusFailed {
			continue
		}
		if c.StartedAt.Before(before) {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeCalls) get(id uuid.UUID) domain.Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.calls[id]
}

type fakeOrgs map[uuid.UUID]*domain.Organization

func (f fakeOrgs) GetOrg(_ context.Context, id uuid.UUID) (*domain.Organization, error) {
	if o, ok := f[id]; ok {
		return o, nil
	}
	return nil, domain.ErrNotFound
}

type fakeEgress struct {
	mu      sync.Mutex
	started []string // room|key
	s3      []*livekit.S3Target
	stopped []string
	err     error
}

func (f *fakeEgress) StartRecording(_ context.Context, room, key string, s3 *livekit.S3Target) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.started = append(f.started, room+"|"+key)
	f.s3 = append(f.s3, s3)
	return "EG_test", nil
}

func (f *fakeEgress) StopRecording(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, id)
	return nil
}

type fakeStore struct {
	mu      sync.Mutex
	objects map[string]int64
	deleted []string
}

func (f *fakeStore) Put(_ context.Context, key, _ string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = int64(len(body))
	return nil
}

func (f *fakeStore) SignedURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	return "https://s3.test/" + key + "?ttl=" + ttl.String(), nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeStore) Stat(_ context.Context, key string) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.objects[key]
	return n, ok, nil
}

type fakeEnt struct{ features map[uuid.UUID]bool }

func (f fakeEnt) CanStartCall(context.Context, uuid.UUID) (bool, string, error) { return true, "", nil }
func (f fakeEnt) HasFeature(_ context.Context, org uuid.UUID, feature string) (bool, error) {
	if feature != Feature {
		return false, errors.New("unexpected feature " + feature)
	}
	return f.features[org], nil
}
func (f fakeEnt) Limits(context.Context, uuid.UUID) (domain.Plan, error) { return domain.Plan{}, nil }

type fakeBus struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *fakeBus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

func (b *fakeBus) all() []domain.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.Event(nil), b.events...)
}

// --- env ---------------------------------------------------------------------

type env struct {
	svc    *Service
	calls  *fakeCalls
	orgs   fakeOrgs
	egress *fakeEgress
	store  *fakeStore
	bus    *fakeBus
	ent    fakeEnt
	org    *domain.Organization
	call   *domain.Call
	now    time.Time
}

func newEnv(t *testing.T, cfg Config) *env {
	t.Helper()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	org := &domain.Organization{ID: uuid.New(), Name: "Org", Settings: map[string]any{}}
	call := &domain.Call{ID: uuid.New(), OrgID: org.ID, RoomName: "call-abc", Status: domain.StatusActive,
		StartedAt: time.Date(2026, 9, 30, 11, 59, 0, 0, time.UTC)}
	e := &env{
		calls: newFakeCalls(call), orgs: fakeOrgs{org.ID: org}, egress: &fakeEgress{},
		store: &fakeStore{objects: map[string]int64{}}, bus: &fakeBus{},
		ent: fakeEnt{features: map[uuid.UUID]bool{org.ID: true}}, org: org, call: call, now: now,
	}
	e.svc = New(e.calls, e.orgs, e.egress, e.store, e.ent, e.bus, cfg, zerolog.Nop())
	e.svc.now = func() time.Time { return e.now }
	return e
}

var enabled = Config{Enabled: true}

// --- tests -------------------------------------------------------------------

func TestOnCallAnsweredStartsRecording(t *testing.T) {
	s3 := &livekit.S3Target{Bucket: "b"}
	e := newEnv(t, Config{Enabled: true, S3: s3})
	c := *e.call
	require.NoError(t, e.svc.OnCallAnswered(context.Background(), &c))

	wantKey := "recordings/" + e.org.ID.String() + "/2026/09/" + e.call.ID.String() + ".ogg"
	require.Equal(t, []string{"call-abc|" + wantKey}, e.egress.started)
	require.Same(t, s3, e.egress.s3[0])

	stored := e.calls.get(e.call.ID)
	require.NotNil(t, stored.Recording)
	require.Equal(t, StatusRecording, stored.Recording.Status)
	require.Equal(t, "EG_test", stored.Recording.EgressID)
	require.Equal(t, wantKey, stored.Recording.ObjectKey)
	require.Equal(t, e.now, *stored.Recording.StartedAt)
	require.Equal(t, StatusRecording, c.Recording.Status, "call updated in place")

	evs := e.bus.all()
	require.Len(t, evs, 1)
	require.Equal(t, domain.EventCallUpdated, evs[0].Type)
	require.Equal(t, e.org.ID, evs[0].OrgID)
	p := evs[0].Payload.(map[string]any)
	require.Equal(t, StatusRecording, p["recording"].(*domain.RecordingInfo).Status)
	require.Equal(t, e.call.ID, p["call"].(*domain.Call).ID)

	// Idempotent.
	require.NoError(t, e.svc.OnCallAnswered(context.Background(), &c))
	require.Len(t, e.egress.started, 1)
}

func TestOnCallAnsweredGating(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		e := newEnv(t, Config{})
		require.NoError(t, e.svc.OnCallAnswered(context.Background(), e.call))
		require.Empty(t, e.egress.started)
	})
	t.Run("feature missing", func(t *testing.T) {
		e := newEnv(t, enabled)
		e.ent.features[e.org.ID] = false
		require.NoError(t, e.svc.OnCallAnswered(context.Background(), e.call))
		require.Empty(t, e.egress.started)
	})
	t.Run("org setting off", func(t *testing.T) {
		for _, v := range []any{false, "false", float64(0)} {
			e := newEnv(t, enabled)
			e.org.Settings[SettingRecordCalls] = v
			require.NoError(t, e.svc.OnCallAnswered(context.Background(), e.call))
			require.Empty(t, e.egress.started, "%v", v)
		}
	})
	t.Run("nil entitlements allow, setting true", func(t *testing.T) {
		e := newEnv(t, enabled)
		e.svc.ent = nil
		e.org.Settings[SettingRecordCalls] = true
		require.NoError(t, e.svc.OnCallAnswered(context.Background(), e.call))
		require.Len(t, e.egress.started, 1)
	})
	t.Run("no room", func(t *testing.T) {
		e := newEnv(t, enabled)
		c := *e.call
		c.RoomName = ""
		require.NoError(t, e.svc.OnCallAnswered(context.Background(), &c))
		require.Empty(t, e.egress.started)
	})
	t.Run("egress error marks failed", func(t *testing.T) {
		e := newEnv(t, enabled)
		e.egress.err = errors.New("egress down")
		require.Error(t, e.svc.OnCallAnswered(context.Background(), e.call))
		require.Equal(t, StatusFailed, e.calls.get(e.call.ID).Recording.Status)
	})
}

func TestOnEgressEndedMarksReady(t *testing.T) {
	e := newEnv(t, enabled)
	c := *e.call
	require.NoError(t, e.svc.OnCallAnswered(context.Background(), &c))
	key := c.Recording.ObjectKey

	e.now = e.now.Add(2 * time.Minute)
	require.NoError(t, e.svc.OnEgressEnded(context.Background(), "call-abc", "EG_test", "ignored-key.ogg", 4096, 118))
	got := e.calls.get(e.call.ID)
	require.Equal(t, StatusReady, got.Recording.Status)
	require.Equal(t, key, got.Recording.ObjectKey, "stored key wins")
	require.EqualValues(t, 4096, got.Recording.SizeBytes)
	require.Equal(t, 118, got.Recording.DurationSec)
	require.Equal(t, e.now, *got.Recording.EndedAt)
	require.Equal(t, URLPath(e.call.ID), got.RecordingURL)
	require.Equal(t, "/api/calls/"+e.call.ID.String()+"/recording", got.RecordingURL)

	evs := e.bus.all()
	last := evs[len(evs)-1].Payload.(map[string]any)
	require.Equal(t, URLPath(e.call.ID), last["call"].(*domain.Call).RecordingURL)

	// Signed URL.
	u, exp, err := e.svc.SignedURL(context.Background(), e.call.ID)
	require.NoError(t, err)
	require.Equal(t, "https://s3.test/"+key+"?ttl=10m0s", u)
	require.Equal(t, e.now.Add(10*time.Minute), exp)

	// Unknown rooms are ignored.
	require.NoError(t, e.svc.OnEgressEnded(context.Background(), "other-room", "EG_x", "x.ogg", 1, 1))
}

func TestOnEgressEndedWithoutStartUsesWebhookKeyAndStat(t *testing.T) {
	e := newEnv(t, enabled)
	e.store.objects["auto/room.ogg"] = 777
	require.NoError(t, e.svc.OnEgressEnded(context.Background(), "call-abc", "EG_auto", "auto/room.ogg", 0, 0))
	got := e.calls.get(e.call.ID)
	require.Equal(t, StatusReady, got.Recording.Status)
	require.Equal(t, "auto/room.ogg", got.Recording.ObjectKey)
	require.EqualValues(t, 777, got.Recording.SizeBytes)
	require.NotNil(t, got.Recording.StartedAt)
}

func TestSignedURLNotReady(t *testing.T) {
	e := newEnv(t, enabled)
	_, _, err := e.svc.SignedURL(context.Background(), e.call.ID)
	require.ErrorIs(t, err, domain.ErrNotFound)
	require.NoError(t, e.svc.OnCallAnswered(context.Background(), e.call))
	_, _, err = e.svc.SignedURL(context.Background(), e.call.ID)
	require.ErrorIs(t, err, domain.ErrNotFound, "still recording")
	_, _, err = e.svc.SignedURL(context.Background(), uuid.New())
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestHandleEgressWebhook(t *testing.T) {
	e := newEnv(t, enabled)
	handled, err := HandleEgressWebhook(context.Background(), e.svc, &lkproto.WebhookEvent{Event: livekit.WebhookRoomFinished})
	require.False(t, handled)
	require.NoError(t, err)

	ev := &lkproto.WebhookEvent{Event: livekit.WebhookEgressEnded, EgressInfo: &lkproto.EgressInfo{
		EgressId: "EG_1", RoomName: "call-abc", Status: lkproto.EgressStatus_EGRESS_COMPLETE,
		FileResults: []*lkproto.FileInfo{{Filename: "/out/recordings/recordings/x.ogg", Size: 10, Duration: 3e9}},
	}}
	handled, err = HandleEgressWebhook(context.Background(), e.svc, ev)
	require.True(t, handled)
	require.NoError(t, err)
	got := e.calls.get(e.call.ID)
	require.Equal(t, StatusReady, got.Recording.Status)
	require.Equal(t, "recordings/x.ogg", got.Recording.ObjectKey)
	require.Equal(t, 3, got.Recording.DurationSec)

	// Failure on another call.
	other := &domain.Call{ID: uuid.New(), OrgID: e.org.ID, RoomName: "call-2", StartedAt: e.now}
	e.calls.calls[other.ID] = other
	ev = &lkproto.WebhookEvent{Event: livekit.WebhookEgressEnded, EgressInfo: &lkproto.EgressInfo{
		EgressId: "EG_2", RoomName: "call-2", Status: lkproto.EgressStatus_EGRESS_FAILED, Error: "boom",
	}}
	handled, err = HandleEgressWebhook(context.Background(), e.svc, ev)
	require.True(t, handled)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, e.calls.get(other.ID).Recording.Status)

	handled, err = HandleEgressWebhook(context.Background(), nil, ev)
	require.False(t, handled)
	require.NoError(t, err)
}

func TestDelete(t *testing.T) {
	e := newEnv(t, enabled)
	require.ErrorIs(t, e.svc.Delete(context.Background(), e.call.ID), domain.ErrNotFound)

	require.NoError(t, e.svc.OnCallAnswered(context.Background(), e.call))
	key := e.calls.get(e.call.ID).Recording.ObjectKey
	e.store.objects[key] = 5
	require.NoError(t, e.svc.OnEgressEnded(context.Background(), "call-abc", "EG_test", key, 5, 1))

	require.NoError(t, e.svc.Delete(context.Background(), e.call.ID))
	got := e.calls.get(e.call.ID)
	require.Equal(t, StatusDeleted, got.Recording.Status)
	require.Empty(t, got.RecordingURL)
	require.Equal(t, []string{key}, e.store.deleted)
	require.ErrorIs(t, e.svc.Delete(context.Background(), e.call.ID), domain.ErrNotFound)
}

func TestDeleteWhileRecordingStopsEgress(t *testing.T) {
	e := newEnv(t, enabled)
	require.NoError(t, e.svc.OnCallAnswered(context.Background(), e.call))
	require.NoError(t, e.svc.Delete(context.Background(), e.call.ID))
	require.Equal(t, []string{"EG_test"}, e.egress.stopped)
}

func TestRunRetention(t *testing.T) {
	e := newEnv(t, Config{Enabled: true, RetentionDays: 90})
	org2 := &domain.Organization{ID: uuid.New(), Settings: map[string]any{SettingRetentionDays: float64(30)}}
	e.orgs[org2.ID] = org2

	mk := func(org uuid.UUID, ageDays int, status string) *domain.Call {
		ended := e.now.AddDate(0, 0, -ageDays)
		c := &domain.Call{ID: uuid.New(), OrgID: org, RoomName: "r-" + uuid.NewString(), StartedAt: ended.Add(-time.Minute),
			Recording: &domain.RecordingInfo{Status: status, ObjectKey: "k/" + uuid.NewString() + ".ogg", EndedAt: &ended}}
		e.calls.calls[c.ID] = c
		e.store.objects[c.Recording.ObjectKey] = 1
		return c
	}
	oldDefault := mk(e.org.ID, 100, StatusReady)  // > 90 → delete
	youngDefault := mk(e.org.ID, 60, StatusReady) // keep (90 days)
	old2 := mk(org2.ID, 45, StatusReady)          // > 30 → delete
	young2 := mk(org2.ID, 10, StatusReady)        // keep
	failedOld := mk(org2.ID, 40, StatusFailed)    // failed with object → delete

	n, err := e.svc.RunRetention(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, n)
	for _, c := range []*domain.Call{oldDefault, old2, failedOld} {
		require.Equal(t, StatusDeleted, e.calls.get(c.ID).Recording.Status)
		_, ok := e.store.objects[c.Recording.ObjectKey]
		require.False(t, ok)
	}
	for _, c := range []*domain.Call{youngDefault, young2} {
		require.Equal(t, StatusReady, e.calls.get(c.ID).Recording.Status)
	}

	n, err = e.svc.RunRetention(context.Background())
	require.NoError(t, err)
	require.Zero(t, n, "second run has nothing to do")
}

func TestRunRetentionPagesBeyondFirstPage(t *testing.T) {
	e := newEnv(t, enabled)
	org2 := &domain.Organization{ID: uuid.New(), Settings: map[string]any{SettingRetentionDays: "7"}}
	e.orgs[org2.ID] = org2
	// A full page of young recordings of the 90-day org, then old ones of the 7-day org.
	for i := 0; i < retentionPage+5; i++ {
		ended := e.now.AddDate(0, 0, -2).Add(-time.Duration(i) * time.Minute)
		c := &domain.Call{ID: uuid.New(), OrgID: e.org.ID, StartedAt: ended,
			Recording: &domain.RecordingInfo{Status: StatusReady, ObjectKey: uuid.NewString(), EndedAt: &ended}}
		e.calls.calls[c.ID] = c
	}
	var old []uuid.UUID
	for i := 0; i < 3; i++ {
		ended := e.now.AddDate(0, 0, -20-i)
		c := &domain.Call{ID: uuid.New(), OrgID: org2.ID, StartedAt: ended,
			Recording: &domain.RecordingInfo{Status: StatusReady, ObjectKey: uuid.NewString(), EndedAt: &ended}}
		e.calls.calls[c.ID] = c
		old = append(old, c.ID)
	}
	n, err := e.svc.RunRetention(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, n)
	for _, id := range old {
		require.Equal(t, StatusDeleted, e.calls.get(id).Recording.Status)
	}
}

func TestObserveBus(t *testing.T) {
	e := newEnv(t, enabled)
	next := &fakeBus{}
	bus := e.svc.ObserveBus(next)

	c := *e.call
	bus.Publish(context.Background(), domain.Event{Type: domain.EventCallAnswered, OrgID: c.OrgID, Payload: map[string]any{"call": &c}})
	bus.Publish(context.Background(), domain.Event{Type: domain.EventCallAnswered, OrgID: c.OrgID, Payload: map[string]any{"call": &c}})
	e.svc.Wait()
	require.Len(t, next.all(), 2, "events forwarded")
	require.Len(t, e.egress.started, 1, "started once")
	require.Equal(t, StatusRecording, e.calls.get(c.ID).Recording.Status)

	bus.Publish(context.Background(), domain.Event{Type: domain.EventCallEnded, OrgID: c.OrgID, Payload: map[string]any{"call": c}})
	e.svc.Wait()
	require.Equal(t, []string{"EG_test"}, e.egress.stopped)

	bus.Publish(context.Background(), domain.Event{Type: domain.EventTranscriptFinal, Payload: map[string]any{}})
	bus.Publish(context.Background(), domain.Event{Type: domain.EventCallAnswered, Payload: map[string]any{}})
	e.svc.Wait()
	require.Len(t, next.all(), 5)
}

func TestSettingsHelpers(t *testing.T) {
	require.True(t, settingBool(nil, "x", true))
	require.False(t, settingBool(map[string]any{"x": "no"}, "x", false))
	require.True(t, settingBool(map[string]any{"x": "garbage"}, "x", true))
	require.Equal(t, 30, settingInt(map[string]any{"x": float64(30)}, "x"))
	require.Equal(t, 0, settingInt(map[string]any{"x": float64(-3)}, "x"))
	require.Equal(t, 12, settingInt(map[string]any{"x": " 12 "}, "x"))
}
