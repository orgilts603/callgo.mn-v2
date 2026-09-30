package postcall_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/postcall"
)

type fakeProfiles struct {
	domain.AgentProfileRepository
	p *domain.AgentProfile
}

func (f fakeProfiles) GetAgentProfile(_ context.Context, id uuid.UUID) (*domain.AgentProfile, error) {
	if f.p == nil || f.p.ID != id {
		return nil, domain.ErrNotFound
	}
	return f.p, nil
}

type fakeContacts struct {
	domain.ContactRepository
	byID    map[uuid.UUID]*domain.Contact
	byPhone map[string]*domain.Contact
}

func (f fakeContacts) GetContact(_ context.Context, id uuid.UUID) (*domain.Contact, error) {
	if c, ok := f.byID[id]; ok {
		return c, nil
	}
	return nil, domain.ErrNotFound
}

func (f fakeContacts) GetContactByPhone(_ context.Context, _ uuid.UUID, phone string) (*domain.Contact, error) {
	if c, ok := f.byPhone[phone]; ok {
		return c, nil
	}
	return nil, domain.ErrNotFound
}

type fakeCampaigns struct {
	domain.CampaignRepository
	c *domain.Campaign
}

func (f fakeCampaigns) GetCampaign(_ context.Context, id uuid.UUID) (*domain.Campaign, error) {
	if f.c != nil && f.c.ID == id {
		return f.c, nil
	}
	return nil, domain.ErrNotFound
}

type sentSMS struct {
	orgID  uuid.UUID
	callID uuid.UUID
	to     string
	body   string
}

type fakeSMS struct {
	mu   sync.Mutex
	sent []sentSMS
	err  error
}

func (f *fakeSMS) Send(_ context.Context, orgID uuid.UUID, callID *uuid.UUID, to, body string) (*domain.SMSMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.sent = append(f.sent, sentSMS{orgID, *callID, to, body})
	return &domain.SMSMessage{To: to, Body: body}, nil
}

type fakeHooks struct {
	mu  sync.Mutex
	ids []uuid.UUID
	evs []domain.Event
	err error
}

func (f *fakeHooks) EnqueueFor(_ context.Context, id uuid.UUID, ev domain.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.ids = append(f.ids, id)
	f.evs = append(f.evs, ev)
	return nil
}

type fakeCallbacks struct {
	mu  sync.Mutex
	cbs []domain.CallbackRequest
}

func (f *fakeCallbacks) CreateCallback(_ context.Context, c *domain.CallbackRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cbs = append(f.cbs, *c)
	return nil
}

var now = time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)

type rig struct {
	org      uuid.UUID
	profile  *domain.AgentProfile
	contact  *domain.Contact
	campaign *domain.Campaign
	sms      *fakeSMS
	hooks    *fakeHooks
	cbs      *fakeCallbacks
	done     []uuid.UUID
	runner   *postcall.Runner
}

func newRig(t *testing.T, actions ...domain.PostCallAction) *rig {
	t.Helper()
	r := &rig{org: uuid.New(), sms: &fakeSMS{}, hooks: &fakeHooks{}, cbs: &fakeCallbacks{}}
	r.profile = &domain.AgentProfile{ID: uuid.New(), OrgID: r.org, PostCallActions: actions}
	r.contact = &domain.Contact{ID: uuid.New(), OrgID: r.org, Phone: "+97699112233", Name: "Болд", Meta: map[string]string{"plan": "gold", "city": "UB"}}
	r.campaign = &domain.Campaign{ID: uuid.New(), OrgID: r.org, Name: "Хураамж", Outcomes: []domain.CampaignOutcome{{Code: "agreed", Label: "Зөвшөөрсөн"}}}
	r.runner = postcall.New(fakeProfiles{p: r.profile},
		fakeContacts{byID: map[uuid.UUID]*domain.Contact{r.contact.ID: r.contact}, byPhone: map[string]*domain.Contact{r.contact.Phone: r.contact}},
		fakeCampaigns{c: r.campaign}, r.sms, r.hooks, r.cbs, zerolog.Nop(),
		postcall.WithClock(func() time.Time { return now }),
		postcall.WithMarkDone(func(_ context.Context, id uuid.UUID) error { r.done = append(r.done, id); return nil }))
	return r
}

func (r *rig) call(mutate ...func(*domain.Call)) *domain.Call {
	c := &domain.Call{
		ID: uuid.New(), OrgID: r.org, AgentProfileID: &r.profile.ID, Direction: domain.DirectionOutbound,
		FromNumber: "+97677001234", ToNumber: "+97699112233", Status: domain.StatusCompleted,
		Summary: "Тохиролцсон", Outcome: "agreed", DurationSec: 42,
	}
	for _, m := range mutate {
		m(c)
	}
	return c
}

func TestSMSTemplateVariables(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionSMS,
		Template: "Сайн уу {{name}}! {{ phone }} / {{summary}} / {{outcome}} / {{campaign}} / {{vars.plan}} / {{vars.custom-key}} / [{{vars.missing}}] [{{unknown}}]"})
	call := r.call(func(c *domain.Call) {
		c.CampaignID = &r.campaign.ID
		c.Metadata = map[string]any{"vars": map[string]any{"custom-key": 7, "plan": "override"}}
	})
	require.NoError(t, r.runner.OnCallEnded(context.Background(), call))
	require.Len(t, r.sms.sent, 1)
	got := r.sms.sent[0]
	require.Equal(t, r.org, got.orgID)
	require.Equal(t, call.ID, got.callID)
	require.Equal(t, "+97699112233", got.to)
	require.Equal(t, "Сайн уу Болд! +97699112233 / Тохиролцсон / Зөвшөөрсөн / Хураамж / override / 7 / [] []", got.body)
	require.Equal(t, []uuid.UUID{call.ID}, r.done)
}

func TestSMSGoSyntaxAndContactMetaVars(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionSMS, Template: "{{.name}} {{vars.city}}"})
	require.NoError(t, r.runner.OnCallEnded(context.Background(), r.call()))
	require.Equal(t, "Болд UB", r.sms.sent[0].body)
}

func TestSMSCustomerNumberByDirection(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionSMS, Template: "hi {{name}}"})
	// Inbound: the customer is the caller (FromNumber); unknown contact -> empty name.
	in := r.call(func(c *domain.Call) {
		c.Direction = domain.DirectionInbound
		c.FromNumber, c.ToNumber = "+97688001122", "+97677001234"
	})
	require.NoError(t, r.runner.OnCallEnded(context.Background(), in))
	require.Equal(t, "+97688001122", r.sms.sent[0].to)
	require.Equal(t, "hi", r.sms.sent[0].body)

	// Contact looked up by phone for an inbound call.
	in2 := r.call(func(c *domain.Call) {
		c.Direction = domain.DirectionInbound
		c.FromNumber = "+97699112233"
	})
	require.NoError(t, r.runner.OnCallEnded(context.Background(), in2))
	require.Equal(t, "hi Болд", r.sms.sent[1].body)
}

func TestOutcomeMatrix(t *testing.T) {
	hook := uuid.New()
	actions := []domain.PostCallAction{
		{Type: domain.ActionSMS, Template: "always"},
		{Type: domain.ActionSMS, Outcomes: []string{"agreed"}, Template: "thanks"},
		{Type: domain.ActionSMS, Outcomes: []string{"declined", "wrong_number"}, Template: "sorry"},
		{Type: domain.ActionWebhook, Outcomes: []string{"agreed"}, WebhookID: &hook},
		{Type: domain.ActionCallback, Outcomes: []string{"callback"}, DelayMin: 30},
	}
	for _, tc := range []struct {
		outcome   string
		wantSMS   []string
		wantHooks int
		wantCBs   int
	}{
		{"agreed", []string{"always", "thanks"}, 1, 0},
		{"declined", []string{"always", "sorry"}, 0, 0},
		{"wrong_number", []string{"always", "sorry"}, 0, 0},
		{"callback", []string{"always"}, 0, 1},
		{"", []string{"always"}, 0, 0},
	} {
		t.Run("outcome="+tc.outcome, func(t *testing.T) {
			r := newRig(t, actions...)
			require.NoError(t, r.runner.OnCallEnded(context.Background(), r.call(func(c *domain.Call) { c.Outcome = tc.outcome })))
			var bodies []string
			for _, s := range r.sms.sent {
				bodies = append(bodies, s.body)
			}
			require.Equal(t, tc.wantSMS, bodies)
			require.Len(t, r.hooks.evs, tc.wantHooks)
			require.Len(t, r.cbs.cbs, tc.wantCBs)
		})
	}
}

func TestWebhookAction(t *testing.T) {
	hook := uuid.New()
	r := newRig(t, domain.PostCallAction{Type: domain.ActionWebhook, WebhookID: &hook})
	call := r.call(func(c *domain.Call) { c.EndReason = "hangup_customer"; c.OutcomeNote = "ok" })
	require.NoError(t, r.runner.OnCallEnded(context.Background(), call))
	require.Equal(t, []uuid.UUID{hook}, r.hooks.ids)
	ev := r.hooks.evs[0]
	require.Equal(t, domain.EventCallEnded, ev.Type)
	require.Equal(t, r.org, ev.OrgID)
	require.Equal(t, call.ID, *ev.CallID)
	require.NotEmpty(t, ev.ID)
	p := ev.Payload.(map[string]any)
	require.Equal(t, call, p["call"])
	require.Equal(t, "agreed", p["outcome"])
	require.Equal(t, "hangup_customer", p["endReason"])
	require.Equal(t, 42, p["durationSec"])

	// Missing webhookId is reported, not panicked on.
	r2 := newRig(t, domain.PostCallAction{Type: domain.ActionWebhook})
	require.ErrorContains(t, r2.runner.OnCallEnded(context.Background(), r2.call()), "no webhookId")
}

func TestCallbackDelay(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionCallback, DelayMin: 45})
	sip := uuid.New()
	call := r.call(func(c *domain.Call) { c.SIPNumberID = &sip })
	require.NoError(t, r.runner.OnCallEnded(context.Background(), call))
	require.Len(t, r.cbs.cbs, 1)
	cb := r.cbs.cbs[0]
	require.Equal(t, now.Add(45*time.Minute), cb.DueAt)
	require.Equal(t, "+97699112233", cb.Phone)
	require.Equal(t, "Болд", cb.Name)
	require.Equal(t, call.ID, *cb.SourceCallID)
	require.Equal(t, r.contact.ID, *cb.ContactID)
	require.Equal(t, sip, *cb.SIPNumberID)
	require.Equal(t, r.profile.ID, *cb.AgentProfileID)
	require.Equal(t, domain.CallbackPending, cb.Status)
	require.Equal(t, "Тохиролцсон", cb.Note)
	require.Equal(t, r.org, cb.OrgID)
}

func TestCallbackFromMetadataWhenDelayZero(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionCallback})
	due := now.Add(3 * time.Hour)
	past := now.Add(-time.Hour)
	call := r.call(func(c *domain.Call) {
		c.Metadata = map[string]any{"callbacks": []any{
			map[string]any{"dueAt": due.Format(time.RFC3339), "note": "ring after lunch"},
			map[string]any{"dueAt": past.Format(time.RFC3339), "note": "late"},
			map[string]any{"note": "no time -> skipped"},
		}}
	})
	require.NoError(t, r.runner.OnCallEnded(context.Background(), call))
	require.Len(t, r.cbs.cbs, 2)
	require.Equal(t, due, r.cbs.cbs[0].DueAt)
	require.Equal(t, "ring after lunch", r.cbs.cbs[0].Note)
	require.Equal(t, now, r.cbs.cbs[1].DueAt, "past due dates are clamped to now")

	// Nothing requested, nothing scheduled.
	r2 := newRig(t, domain.PostCallAction{Type: domain.ActionCallback})
	require.NoError(t, r2.runner.OnCallEnded(context.Background(), r2.call()))
	require.Empty(t, r2.cbs.cbs)

	// Malformed metadata is an error, not a crash.
	r3 := newRig(t, domain.PostCallAction{Type: domain.ActionCallback})
	err := r3.runner.OnCallEnded(context.Background(), r3.call(func(c *domain.Call) { c.Metadata = map[string]any{"callbacks": "nope"} }))
	require.Error(t, err)
}

func TestIdempotency(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionSMS, Template: "x"})
	call := r.call()
	require.NoError(t, r.runner.OnCallEnded(context.Background(), call))
	require.NoError(t, r.runner.OnCallEnded(context.Background(), call))
	require.Len(t, r.sms.sent, 1, "second run is a no-op (in-memory cache)")

	// A fresh runner (restart) honours the persisted flag.
	r2 := newRig(t, domain.PostCallAction{Type: domain.ActionSMS, Template: "x"})
	done := r2.call(func(c *domain.Call) { c.Metadata = map[string]any{"postCallDone": true} })
	require.NoError(t, r2.runner.OnCallEnded(context.Background(), done))
	require.Empty(t, r2.sms.sent)
	require.Empty(t, r2.done)
}

func TestConcurrentEndedRunsOnce(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionSMS, Template: "x"})
	call := r.call()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = r.runner.OnCallEnded(context.Background(), call) }()
	}
	wg.Wait()
	require.Len(t, r.sms.sent, 1)
}

func TestCacheEvictionAllowsReprocessing(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionSMS, Template: "x"})
	runner := postcall.New(fakeProfiles{p: r.profile}, nil, nil, r.sms, nil, nil, zerolog.Nop(), postcall.WithCacheSize(1))
	first, second := r.call(), r.call()
	require.NoError(t, runner.OnCallEnded(context.Background(), first))
	require.NoError(t, runner.OnCallEnded(context.Background(), second))
	require.NoError(t, runner.OnCallEnded(context.Background(), first)) // evicted
	require.Len(t, r.sms.sent, 3)
}

func TestFailuresAreIsolatedAndJoined(t *testing.T) {
	hook := uuid.New()
	r := newRig(t,
		domain.PostCallAction{Type: domain.ActionSMS, Template: "x"},
		domain.PostCallAction{Type: domain.ActionWebhook, WebhookID: &hook},
		domain.PostCallAction{Type: domain.ActionCallback, DelayMin: 5},
	)
	r.sms.err = errors.New("gateway down")
	err := r.runner.OnCallEnded(context.Background(), r.call())
	require.ErrorContains(t, err, "gateway down")
	require.Len(t, r.hooks.evs, 1, "webhook still ran")
	require.Len(t, r.cbs.cbs, 1, "callback still ran")
	require.Len(t, r.done, 1, "call is marked done even when an action failed")
}

func TestNoopCases(t *testing.T) {
	r := newRig(t)
	require.NoError(t, r.runner.OnCallEnded(context.Background(), nil))
	require.NoError(t, r.runner.OnCallEnded(context.Background(), r.call(func(c *domain.Call) { c.AgentProfileID = nil })))
	require.NoError(t, r.runner.OnCallEnded(context.Background(), r.call())) // no actions
	require.Empty(t, r.done)
	// Unknown profile is an error.
	missing := uuid.New()
	require.ErrorIs(t, r.runner.OnCallEnded(context.Background(), r.call(func(c *domain.Call) { c.AgentProfileID = &missing })), domain.ErrNotFound)
}

func TestSMSSkipsEmptyRenderedBodyAndMissingPhone(t *testing.T) {
	r := newRig(t, domain.PostCallAction{Type: domain.ActionSMS, Template: "{{vars.nothing}}"})
	require.ErrorContains(t, r.runner.OnCallEnded(context.Background(), r.call()), "empty")
	require.Empty(t, r.sms.sent)

	r2 := newRig(t, domain.PostCallAction{Type: domain.ActionSMS, Template: "hi"})
	err := r2.runner.OnCallEnded(context.Background(), r2.call(func(c *domain.Call) { c.ToNumber = "" }))
	require.ErrorContains(t, err, "no customer number")

	_, err = postcall.RenderTemplate("{{", nil)
	require.Error(t, err)
}

func TestNilCollaboratorsAreReported(t *testing.T) {
	hook := uuid.New()
	p := &domain.AgentProfile{ID: uuid.New(), PostCallActions: []domain.PostCallAction{
		{Type: domain.ActionSMS, Template: "x"}, {Type: domain.ActionWebhook, WebhookID: &hook}, {Type: domain.ActionCallback, DelayMin: 1},
	}}
	runner := postcall.New(fakeProfiles{p: p}, nil, nil, nil, nil, nil, zerolog.Nop())
	err := runner.OnCallEnded(context.Background(), &domain.Call{ID: uuid.New(), AgentProfileID: &p.ID, ToNumber: "+97699112233", Direction: domain.DirectionOutbound})
	require.ErrorContains(t, err, "sms is not configured")
	require.ErrorContains(t, err, "webhooks are not configured")
	require.ErrorContains(t, err, "callbacks are not configured")
}
