package crm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func newWebhook(t *testing.T, ctx context.Context, s *Store, orgID uuid.UUID, active bool, events ...string) *domain.Webhook {
	t.Helper()
	w := &domain.Webhook{OrgID: orgID, URL: "https://hooks.example.mn/" + uuid.NewString(),
		Secret: "whsec_" + uuid.NewString(), Events: events, Active: active}
	require.NoError(t, s.CreateWebhook(ctx, w))
	return w
}

func TestWebhookCRUDAndSecret(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "wh")

	w := &domain.Webhook{OrgID: org.ID, URL: "https://crm.example.mn/hook", Secret: "whsec_abcdefgh1234",
		Events: []string{string(domain.EventCallEnded)}, Active: true, Description: "CRM"}
	require.NoError(t, s.CreateWebhook(ctx, w))
	require.NotEqual(t, uuid.Nil, w.ID)
	require.Equal(t, "…1234", w.SecretHint)
	require.False(t, w.CreatedAt.IsZero())

	var enc string
	require.NoError(t, testPool.QueryRow(ctx, `SELECT secret_enc FROM webhooks WHERE id = $1`, w.ID).Scan(&enc))
	require.NotEmpty(t, enc)
	require.NotContains(t, enc, "abcdefgh1234", "secret encrypted at rest")

	got, err := s.GetWebhook(ctx, w.ID)
	require.NoError(t, err)
	require.Equal(t, "whsec_abcdefgh1234", got.Secret)
	require.Equal(t, "…1234", got.SecretHint)
	require.Equal(t, w.Events, got.Events)
	require.Equal(t, "CRM", got.Description)
	require.True(t, got.Active)

	list, err := s.ListWebhooks(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Empty(t, list[0].Secret, "list never exposes the secret")
	require.Equal(t, "…1234", list[0].SecretHint)

	// Update without a secret keeps it; health fields are persisted.
	lastAt := time.Now().UTC().Truncate(time.Microsecond)
	got.Secret = ""
	got.URL = "https://crm.example.mn/v2"
	got.Events = []string{"*"}
	got.FailureCount, got.LastStatus, got.LastAt = 3, 502, &lastAt
	require.NoError(t, s.UpdateWebhook(ctx, got))
	require.Equal(t, "…1234", got.SecretHint)
	again, err := s.GetWebhook(ctx, w.ID)
	require.NoError(t, err)
	require.Equal(t, "whsec_abcdefgh1234", again.Secret)
	require.Equal(t, "https://crm.example.mn/v2", again.URL)
	require.Equal(t, []string{"*"}, again.Events)
	require.Equal(t, 3, again.FailureCount)
	require.Equal(t, 502, again.LastStatus)
	require.True(t, lastAt.Equal(*again.LastAt))

	// Rotation.
	again.Secret = "whsec_rotated_zzzz9999"
	require.NoError(t, s.UpdateWebhook(ctx, again))
	require.Equal(t, "…9999", again.SecretHint)
	rot, err := s.GetWebhook(ctx, w.ID)
	require.NoError(t, err)
	require.Equal(t, "whsec_rotated_zzzz9999", rot.Secret)

	// Nil events are stored as an empty array.
	rot.Events = nil
	require.NoError(t, s.UpdateWebhook(ctx, rot))
	rot, err = s.GetWebhook(ctx, w.ID)
	require.NoError(t, err)
	require.Equal(t, []string{}, rot.Events)

	requireErrIs(t, s.UpdateWebhook(ctx, &domain.Webhook{ID: uuid.New()}), domain.ErrNotFound)
	require.NoError(t, s.DeleteWebhook(ctx, w.ID))
	requireErrIs(t, s.DeleteWebhook(ctx, w.ID), domain.ErrNotFound)
	_, err = s.GetWebhook(ctx, w.ID)
	requireErrIs(t, err, domain.ErrNotFound)
	requireErrIs(t, s.CreateWebhook(ctx, &domain.Webhook{OrgID: uuid.New(), URL: "x"}), domain.ErrInvalid)
}

func TestWebhookSecretHint(t *testing.T) {
	require.Equal(t, "", webhookSecretHint(""))
	require.Equal(t, "…", webhookSecretHint("short"))
	require.Equal(t, "…5678", webhookSecretHint("12345678"))
}

func TestListActiveWebhooksForEvent(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "whev")
	other := newOrg(t, ctx, s, "whev2")

	ended := newWebhook(t, ctx, s, org.ID, true, string(domain.EventCallEnded))
	all := newWebhook(t, ctx, s, org.ID, true, "*")
	multi := newWebhook(t, ctx, s, org.ID, true, string(domain.EventCallStarted), string(domain.EventCallEnded))
	started := newWebhook(t, ctx, s, org.ID, true, string(domain.EventCallStarted))
	newWebhook(t, ctx, s, org.ID, false, string(domain.EventCallEnded), "*")  // inactive
	newWebhook(t, ctx, s, org.ID, true)                                       // no events
	newWebhook(t, ctx, s, other.ID, true, string(domain.EventCallEnded), "*") // other org

	ids := func(ws []domain.Webhook) []uuid.UUID {
		out := make([]uuid.UUID, len(ws))
		for i, w := range ws {
			out[i] = w.ID
			require.NotEmpty(t, w.Secret, "dispatcher needs the signing secret")
		}
		return out
	}

	got, err := s.ListActiveWebhooksForEvent(ctx, org.ID, domain.EventCallEnded)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{ended.ID, all.ID, multi.ID}, ids(got))

	got, err = s.ListActiveWebhooksForEvent(ctx, org.ID, domain.EventCallStarted)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{all.ID, multi.ID, started.ID}, ids(got))

	got, err = s.ListActiveWebhooksForEvent(ctx, org.ID, domain.EventCampaignProgress)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{all.ID}, ids(got))

	got, err = s.ListActiveWebhooksForEvent(ctx, uuid.New(), domain.EventCallEnded)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestWebhookDeliveries(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "whd")
	w := newWebhook(t, ctx, s, org.ID, true, "*")

	for i := 0; i < 5; i++ {
		d := &domain.WebhookDelivery{WebhookID: w.ID, EventID: uuid.NewString(), EventType: domain.EventCallEnded,
			Payload: []byte(`{"n":` + string(rune('0'+i)) + `}`)}
		require.NoError(t, s.CreateDelivery(ctx, d))
		require.Equal(t, DeliveryPending, d.Status)
	}
	list, total, err := s.ListDeliveries(ctx, w.ID, 2, 0)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Len(t, list, 2)
	require.Equal(t, []byte(`{"n":4}`), list[0].Payload, "newest first")
	_, _, err = s.ListDeliveries(ctx, w.ID, 2, 4)
	require.NoError(t, err)
	empty, total, err := s.ListDeliveries(ctx, w.ID, 2, 10)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Empty(t, empty)

	d := list[0]
	next := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	d.Status, d.Attempts, d.ResponseCode, d.LastError, d.NextTryAt = DeliveryPending, 1, 500, "boom", &next
	require.NoError(t, s.UpdateDelivery(ctx, &d))
	got, _, err := s.ListDeliveries(ctx, w.ID, 1, 0)
	require.NoError(t, err)
	require.Equal(t, 500, got[0].ResponseCode)
	require.Equal(t, "boom", got[0].LastError)
	require.True(t, next.Equal(*got[0].NextTryAt))

	d.Status = "bogus"
	requireErrIs(t, s.UpdateDelivery(ctx, &d), domain.ErrInvalid)
	requireErrIs(t, s.UpdateDelivery(ctx, &domain.WebhookDelivery{ID: uuid.New(), Status: DeliveryFailed}),
		domain.ErrNotFound)

	// Deleting the webhook removes its log.
	require.NoError(t, s.DeleteWebhook(ctx, w.ID))
	_, total, err = s.ListDeliveries(ctx, w.ID, 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)
}

func TestClaimDueDeliveriesNoDoubleClaim(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "claimd")
	w := newWebhook(t, ctx, s, org.ID, true, "*")

	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	const due = 60
	want := map[uuid.UUID]bool{}
	for i := 0; i < due; i++ {
		d := &domain.WebhookDelivery{WebhookID: w.ID, EventType: domain.EventCallEnded, Payload: []byte("{}")}
		if i%2 == 0 {
			d.NextTryAt = &past
		}
		require.NoError(t, s.CreateDelivery(ctx, d))
		want[d.ID] = true
	}
	// Not claimable: scheduled later, already delivered, already failed.
	require.NoError(t, s.CreateDelivery(ctx, &domain.WebhookDelivery{WebhookID: w.ID, NextTryAt: &future}))
	require.NoError(t, s.CreateDelivery(ctx, &domain.WebhookDelivery{WebhookID: w.ID, Status: DeliveryDelivered}))
	require.NoError(t, s.CreateDelivery(ctx, &domain.WebhookDelivery{WebhookID: w.ID, Status: DeliveryFailed}))

	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				batch, err := s.ClaimDueDeliveries(ctx, 4)
				if err != nil {
					errs <- err
					return
				}
				if len(batch) == 0 {
					return
				}
				mu.Lock()
				for _, d := range batch {
					seen[d.ID]++
					if d.Attempts != 1 || d.NextTryAt == nil || !d.NextTryAt.After(time.Now()) {
						errs <- errBadClaim
					}
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, seen, due)
	for id, n := range seen {
		require.Truef(t, want[id], "unexpected delivery %s claimed", id)
		require.Equalf(t, 1, n, "delivery %s claimed %d times", id, n)
	}

	// Leased: nothing is due right now.
	batch, err := s.ClaimDueDeliveries(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, batch)
	none, err := s.ClaimDueDeliveries(ctx, 0)
	require.NoError(t, err)
	require.Empty(t, none)

	// A worker reschedules one for retry in the past → claimable again.
	var id uuid.UUID
	for k := range seen {
		id = k
		break
	}
	require.NoError(t, s.UpdateDelivery(ctx, &domain.WebhookDelivery{ID: id, Status: DeliveryPending,
		Attempts: 1, NextTryAt: &past}))
	batch, err = s.ClaimDueDeliveries(ctx, 100)
	require.NoError(t, err)
	require.Len(t, batch, 1)
	require.Equal(t, id, batch[0].ID)
	require.Equal(t, 2, batch[0].Attempts)
}

var errBadClaim = errors.New("claimed delivery has wrong attempts or lease")

func TestSMSMessages(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "sms")
	c := newCall(t, ctx, s, domain.Call{OrgID: org.ID})

	m := &domain.SMSMessage{OrgID: org.ID, CallID: &c.ID, To: "+97699112233", Body: "Баярлалаа", Provider: "mock"}
	require.NoError(t, s.CreateSMS(ctx, m))
	require.Equal(t, SMSQueued, m.Status)
	require.False(t, m.CreatedAt.IsZero())

	sent := time.Now().UTC().Truncate(time.Microsecond)
	m.Status, m.ProviderRef, m.SentAt = SMSSent, "ref-1", &sent
	require.NoError(t, s.UpdateSMS(ctx, m))
	for i := 0; i < 3; i++ {
		require.NoError(t, s.CreateSMS(ctx, &domain.SMSMessage{OrgID: org.ID, To: "+97699000000", Body: "x"}))
	}
	require.NoError(t, s.CreateSMS(ctx, &domain.SMSMessage{OrgID: newOrg(t, ctx, s, "sms2").ID, To: "+1", Body: "y"}))

	list, total, err := s.ListSMS(ctx, org.ID, 10, 0)
	require.NoError(t, err)
	require.Equal(t, 4, total)
	require.Len(t, list, 4)
	got := list[3] // oldest last
	require.Equal(t, m.ID, got.ID)
	require.Equal(t, SMSSent, got.Status)
	require.Equal(t, "ref-1", got.ProviderRef)
	require.Equal(t, &c.ID, got.CallID)
	require.True(t, sent.Equal(*got.SentAt))

	page, total, err := s.ListSMS(ctx, org.ID, 2, 2)
	require.NoError(t, err)
	require.Equal(t, 4, total)
	require.Len(t, page, 2)

	m.Status = "bogus"
	requireErrIs(t, s.UpdateSMS(ctx, m), domain.ErrInvalid)
	requireErrIs(t, s.UpdateSMS(ctx, &domain.SMSMessage{ID: uuid.New(), Status: SMSFailed}), domain.ErrNotFound)
}

func TestCallbacksCRUD(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "cb")
	src := newCall(t, ctx, s, domain.Call{OrgID: org.ID})
	p := &domain.AgentProfile{OrgID: org.ID, Name: "cb"}
	require.NoError(t, s.CreateAgentProfile(ctx, p))

	due := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	cb := &domain.CallbackRequest{OrgID: org.ID, SourceCallID: &src.ID, Phone: "+97699001122", Name: "Бат",
		Note: "маргааш залгана", DueAt: due, AgentProfileID: &p.ID}
	require.NoError(t, s.CreateCallback(ctx, cb))
	require.Equal(t, domain.CallbackPending, cb.Status)

	got, err := s.GetCallback(ctx, cb.ID)
	require.NoError(t, err)
	require.True(t, due.Equal(got.DueAt))
	require.Equal(t, &src.ID, got.SourceCallID)
	require.Equal(t, &p.ID, got.AgentProfileID)
	require.Equal(t, "Бат", got.Name)

	// Zero DueAt defaults to now.
	now := &domain.CallbackRequest{OrgID: org.ID, Phone: "+97699000001"}
	require.NoError(t, s.CreateCallback(ctx, now))
	require.WithinDuration(t, time.Now(), now.DueAt, time.Minute)

	res := newCall(t, ctx, s, domain.Call{OrgID: org.ID})
	got.Status, got.ResultCallID, got.Attempts, got.Note = domain.CallbackDone, &res.ID, 2, "done"
	got.DueAt = time.Time{} // keeps stored due time
	require.NoError(t, s.UpdateCallback(ctx, got))
	require.True(t, due.Equal(got.DueAt))
	again, err := s.GetCallback(ctx, cb.ID)
	require.NoError(t, err)
	require.Equal(t, domain.CallbackDone, again.Status)
	require.Equal(t, &res.ID, again.ResultCallID)
	require.Equal(t, 2, again.Attempts)
	require.Equal(t, "done", again.Note)

	all, total, err := s.ListCallbacks(ctx, org.ID, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, all, 2)
	pending, total, err := s.ListCallbacks(ctx, org.ID, domain.CallbackPending, 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, now.ID, pending[0].ID)
	_, total, err = s.ListCallbacks(ctx, org.ID, domain.CallbackCanceled, 10, 0)
	require.NoError(t, err)
	require.Zero(t, total)

	again.Status = "bogus"
	requireErrIs(t, s.UpdateCallback(ctx, again), domain.ErrInvalid)
	requireErrIs(t, s.UpdateCallback(ctx, &domain.CallbackRequest{ID: uuid.New(), Status: domain.CallbackDone}),
		domain.ErrNotFound)
	_, err = s.GetCallback(ctx, uuid.New())
	requireErrIs(t, err, domain.ErrNotFound)
}

func TestClaimDueCallbacks(t *testing.T) {
	ctx, s := setup(t)
	org := newOrg(t, ctx, s, "cbclaim")
	base := time.Now().UTC().Add(-time.Hour)

	const due = 40
	want := map[uuid.UUID]bool{}
	for i := 0; i < due; i++ {
		cb := &domain.CallbackRequest{OrgID: org.ID, Phone: "+9769900" + uuid.NewString()[:4],
			DueAt: base.Add(time.Duration(i) * time.Second)}
		require.NoError(t, s.CreateCallback(ctx, cb))
		want[cb.ID] = true
	}
	// Not claimable: future, canceled, done.
	require.NoError(t, s.CreateCallback(ctx, &domain.CallbackRequest{OrgID: org.ID, Phone: "+1",
		DueAt: time.Now().Add(time.Hour)}))
	require.NoError(t, s.CreateCallback(ctx, &domain.CallbackRequest{OrgID: org.ID, Phone: "+2",
		DueAt: base, Status: domain.CallbackCanceled}))
	require.NoError(t, s.CreateCallback(ctx, &domain.CallbackRequest{OrgID: org.ID, Phone: "+3",
		DueAt: base, Status: domain.CallbackDone}))

	// A single small claim returns the soonest-due first.
	first, err := s.ClaimDueCallbacks(ctx, 3)
	require.NoError(t, err)
	require.Len(t, first, 3)
	for i := 1; i < len(first); i++ {
		require.False(t, first[i].DueAt.Before(first[i-1].DueAt))
	}
	require.WithinDuration(t, base, first[0].DueAt, time.Millisecond)

	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	for _, c := range first {
		seen[c.ID]++
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				batch, err := s.ClaimDueCallbacks(ctx, 3)
				if err != nil {
					errs <- err
					return
				}
				if len(batch) == 0 {
					return
				}
				mu.Lock()
				for _, c := range batch {
					seen[c.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, seen, due)
	for id, n := range seen {
		require.True(t, want[id])
		require.Equalf(t, 1, n, "callback %s claimed %d times", id, n)
		got, err := s.GetCallback(ctx, id)
		require.NoError(t, err)
		require.Equal(t, domain.CallbackDialed, got.Status)
		require.Equal(t, 1, got.Attempts)
	}

	rest, err := s.ClaimDueCallbacks(ctx, 100)
	require.NoError(t, err)
	require.Empty(t, rest)
	_, total, err := s.ListCallbacks(ctx, org.ID, domain.CallbackDialed, 1, 0)
	require.NoError(t, err)
	require.Equal(t, due, total)
}
