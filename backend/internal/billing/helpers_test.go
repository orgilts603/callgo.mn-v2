package billing_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/billing/memory"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/payments/mock"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type bus struct {
	mu     sync.Mutex
	events []domain.Event
}

func (b *bus) Publish(_ context.Context, ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev)
}

func (b *bus) ofType(t domain.EventType) []domain.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []domain.Event
	for _, e := range b.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	clock *clock
	store *memory.Store
	bus   *bus
	pay   *mock.Provider
	svc   *billing.Service
	org   domain.Organization
}

var t0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func newFixture(t *testing.T, mutate ...func(*billing.Config)) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), clock: &clock{t: t0}, store: memory.New(), bus: &bus{}, pay: mock.New()}
	f.pay.Now = f.clock.Now
	cfg := billing.Config{
		Now: f.clock.Now,
		ProviderCosts: billing.ProviderCosts{
			LLMPer1kTokensMNT: 10, STTPerMinMNT: 60, TTSPer1kCharsMNT: 50, SIPPerMinMNT: 20, SMSPerMsgMNT: 45,
		},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	f.svc = billing.New(f.store, f.store, f.pay, f.bus, cfg, zerolog.Nop())
	f.org = f.newOrg("Demo")
	return f
}

func (f *fixture) newOrg(name string) domain.Organization {
	o := domain.Organization{ID: uuid.New(), Name: name, Slug: name, Status: domain.OrgActive, Timezone: "Asia/Ulaanbaatar", CreatedAt: f.clock.Now()}
	f.store.PutOrg(o)
	return o
}

func (f *fixture) sub(orgID uuid.UUID) *domain.Subscription {
	f.t.Helper()
	s, err := f.store.GetSubscription(f.ctx, orgID)
	require.NoError(f.t, err)
	return s
}

func (f *fixture) orgStatus(orgID uuid.UUID) domain.Organization {
	f.t.Helper()
	o, err := f.store.GetOrg(f.ctx, orgID)
	require.NoError(f.t, err)
	return *o
}

// paidSub puts org on a paid plan with its first invoice paid.
func (f *fixture) paidSub(orgID uuid.UUID, plan string) *domain.Invoice {
	f.t.Helper()
	_, err := f.svc.StartTrial(f.ctx, orgID)
	require.NoError(f.t, err)
	res, err := f.svc.ChangePlan(f.ctx, orgID, plan)
	require.NoError(f.t, err)
	require.NotNil(f.t, res.Invoice)
	inv, err := f.svc.MarkPaidManually(f.ctx, res.Invoice.ID, "test")
	require.NoError(f.t, err)
	return inv
}

// callMinutes records one answered call of the given minutes at now.
func (f *fixture) callMinutes(orgID uuid.UUID, minutes int) {
	f.t.Helper()
	now := f.clock.Now()
	ans := now.Add(-time.Duration(minutes) * time.Minute)
	require.NoError(f.t, f.svc.RecordCall(f.ctx, &domain.Call{
		ID: uuid.New(), OrgID: orgID, StartedAt: ans, AnsweredAt: &ans, EndedAt: &now, DurationSec: minutes * 60,
	}))
}

func zeroLog() zerolog.Logger { return zerolog.Nop() }
