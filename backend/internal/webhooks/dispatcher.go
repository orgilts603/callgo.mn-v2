package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// ErrWebhookInactive is returned when an operation needs an active webhook.
var ErrWebhookInactive = errors.New("webhook is inactive")

// Config tunes a Dispatcher. Zero values select the documented defaults.
type Config struct {
	// Workers is the number of concurrent deliveries (default 4).
	Workers int
	// Timeout bounds one HTTP attempt (default 10s).
	Timeout time.Duration
	// Backoff[i] is the wait before retry i+1 (default 1m, 5m, 30m, 2h, 12h).
	Backoff []time.Duration
	// MaxAttempts is the total number of attempts before a delivery is
	// marked failed (default len(Backoff)+1: the first try plus every retry).
	MaxAttempts int
	// MaxFailuresToDisable consecutive failed attempts disable the webhook
	// (default 20).
	MaxFailuresToDisable int
	// PollInterval is how often Run looks for due deliveries when idle
	// (default 2s). Enqueue wakes Run immediately.
	PollInterval time.Duration
	// Now is the clock (default time.Now); tests inject a fake one.
	Now func() time.Time
}

// DefaultBackoff is the retry schedule of docs/API.md.
var DefaultBackoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 12 * time.Hour}

func (c *Config) defaults() {
	if c.Workers <= 0 {
		c.Workers = 4
	}
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if len(c.Backoff) == 0 {
		c.Backoff = slices.Clone(DefaultBackoff)
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = len(c.Backoff) + 1
	}
	if c.MaxFailuresToDisable <= 0 {
		c.MaxFailuresToDisable = 20
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Dispatcher turns events into webhook deliveries and sends them.
type Dispatcher struct {
	repo   domain.IntegrationsRepository
	client *http.Client
	cfg    Config
	bus    domain.EventBus
	log    zerolog.Logger

	wake chan struct{}
	// statsMu serialises webhook failure-counter read-modify-write cycles.
	statsMu sync.Mutex
}

// New builds a Dispatcher. client may be nil (a default client is used); its
// redirects are never followed. bus may be nil (webhook.failed is then only
// logged).
func New(repo domain.IntegrationsRepository, client *http.Client, cfg Config, bus domain.EventBus, log zerolog.Logger) *Dispatcher {
	cfg.defaults()
	if client == nil {
		client = &http.Client{}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Dispatcher{
		repo: repo, client: &c, cfg: cfg, bus: bus,
		log:  log.With().Str("component", "webhooks").Logger(),
		wake: make(chan struct{}, 1),
	}
}

func (d *Dispatcher) now() time.Time { return d.cfg.Now().UTC() }

func (d *Dispatcher) kick() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Enqueue creates a pending delivery for every active webhook of ev.OrgID
// that subscribed to ev.Type. Events that are not deliverable (see
// KnownEvents) are ignored. It returns the first repository error after
// attempting all webhooks.
func (d *Dispatcher) Enqueue(ctx context.Context, ev domain.Event) error {
	if !Deliverable(ev.Type) {
		return nil
	}
	ev = d.stamp(ev)
	hooks, err := d.repo.ListActiveWebhooksForEvent(ctx, ev.OrgID, ev.Type)
	if err != nil {
		return fmt.Errorf("list webhooks for %s: %w", ev.Type, err)
	}
	if len(hooks) == 0 {
		return nil
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event %s: %w", ev.Type, err)
	}
	var errs []error
	for i := range hooks {
		h := &hooks[i]
		if !h.Active || !subscribed(h.Events, ev.Type) {
			continue
		}
		if _, err := d.createDelivery(ctx, h.ID, ev, payload); err != nil {
			errs = append(errs, err)
		}
	}
	d.kick()
	return errors.Join(errs...)
}

// EnqueueFor queues ev for one specific webhook regardless of its event
// subscription (used by post-call actions). The webhook must belong to
// ev.OrgID and be active.
func (d *Dispatcher) EnqueueFor(ctx context.Context, webhookID uuid.UUID, ev domain.Event) error {
	w, err := d.repo.GetWebhook(ctx, webhookID)
	if err != nil {
		return fmt.Errorf("get webhook %s: %w", webhookID, err)
	}
	if w.OrgID != ev.OrgID {
		return fmt.Errorf("webhook %s: %w", webhookID, domain.ErrNotFound)
	}
	if !w.Active {
		return fmt.Errorf("webhook %s: %w", webhookID, ErrWebhookInactive)
	}
	ev = d.stamp(ev)
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal event %s: %w", ev.Type, err)
	}
	if _, err := d.createDelivery(ctx, w.ID, ev, payload); err != nil {
		return err
	}
	d.kick()
	return nil
}

func (d *Dispatcher) stamp(ev domain.Event) domain.Event {
	if ev.ID == "" {
		ev.ID = uuid.NewString()
	}
	if ev.At.IsZero() {
		ev.At = d.now()
	}
	return ev
}

func subscribed(events []string, t domain.EventType) bool {
	return slices.Contains(events, Wildcard) || slices.Contains(events, string(t))
}

func (d *Dispatcher) createDelivery(ctx context.Context, webhookID uuid.UUID, ev domain.Event, payload []byte) (*domain.WebhookDelivery, error) {
	now := d.now()
	dl := &domain.WebhookDelivery{
		ID: uuid.New(), WebhookID: webhookID, EventID: ev.ID, EventType: ev.Type,
		Status: "pending", NextTryAt: &now, Payload: payload, CreatedAt: now, UpdatedAt: now,
	}
	if err := d.repo.CreateDelivery(ctx, dl); err != nil {
		return nil, fmt.Errorf("create delivery for webhook %s: %w", webhookID, err)
	}
	return dl, nil
}

// Test sends a synthetic `system` event to the webhook right now (a single
// attempt, no retries, even when the webhook is inactive) and returns the
// resulting delivery record.
func (d *Dispatcher) Test(ctx context.Context, webhookID uuid.UUID) (*domain.WebhookDelivery, error) {
	w, err := d.repo.GetWebhook(ctx, webhookID)
	if err != nil {
		return nil, fmt.Errorf("get webhook %s: %w", webhookID, err)
	}
	ev := d.stamp(domain.Event{
		Type: domain.EventSystem, OrgID: w.OrgID,
		Payload: map[string]any{"message": "CallGo webhook test", "test": true, "webhookId": w.ID},
	})
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("marshal test event: %w", err)
	}
	dl, err := d.createDelivery(ctx, w.ID, ev, payload)
	if err != nil {
		return nil, err
	}
	d.process(ctx, dl, true)
	return dl, nil
}

// Retry re-queues a failed (or pending) delivery of a webhook of orgID with a
// fresh attempt budget; Run sends it on its next pass. The webhook must be
// active.
func (d *Dispatcher) Retry(ctx context.Context, orgID, deliveryID uuid.UUID) (*domain.WebhookDelivery, error) {
	dl, w, err := d.findDelivery(ctx, orgID, deliveryID)
	if err != nil {
		return nil, err
	}
	if !w.Active {
		return nil, fmt.Errorf("webhook %s: %w", w.ID, ErrWebhookInactive)
	}
	now := d.now()
	dl.Status = "pending"
	dl.Attempts = 0
	dl.LastError = ""
	dl.NextTryAt = &now
	dl.UpdatedAt = now
	if err := d.repo.UpdateDelivery(ctx, dl); err != nil {
		return nil, fmt.Errorf("update delivery %s: %w", dl.ID, err)
	}
	d.kick()
	return dl, nil
}

// deliveryGetter is implemented by repositories that can load a delivery by
// id; otherwise findDelivery scans the org's webhooks.
type deliveryGetter interface {
	GetDelivery(ctx context.Context, id uuid.UUID) (*domain.WebhookDelivery, error)
}

const (
	scanPageSize = 100
	scanMaxPages = 20
)

func (d *Dispatcher) findDelivery(ctx context.Context, orgID, id uuid.UUID) (*domain.WebhookDelivery, *domain.Webhook, error) {
	if g, ok := d.repo.(deliveryGetter); ok {
		dl, err := g.GetDelivery(ctx, id)
		if err != nil {
			return nil, nil, fmt.Errorf("get delivery %s: %w", id, err)
		}
		w, err := d.repo.GetWebhook(ctx, dl.WebhookID)
		if err != nil {
			return nil, nil, fmt.Errorf("get webhook %s: %w", dl.WebhookID, err)
		}
		if w.OrgID != orgID {
			return nil, nil, fmt.Errorf("delivery %s: %w", id, domain.ErrNotFound)
		}
		return dl, w, nil
	}
	hooks, err := d.repo.ListWebhooks(ctx, orgID)
	if err != nil {
		return nil, nil, fmt.Errorf("list webhooks: %w", err)
	}
	for i := range hooks {
		for page := 0; page < scanMaxPages; page++ {
			items, total, err := d.repo.ListDeliveries(ctx, hooks[i].ID, scanPageSize, page*scanPageSize)
			if err != nil {
				return nil, nil, fmt.Errorf("list deliveries: %w", err)
			}
			for j := range items {
				if items[j].ID == id {
					dl := items[j]
					w, err := d.repo.GetWebhook(ctx, hooks[i].ID)
					if err != nil {
						return nil, nil, fmt.Errorf("get webhook %s: %w", hooks[i].ID, err)
					}
					return &dl, w, nil
				}
			}
			if (page+1)*scanPageSize >= total {
				break
			}
		}
	}
	return nil, nil, fmt.Errorf("delivery %s: %w", id, domain.ErrNotFound)
}

// Run claims due deliveries and sends them until ctx is cancelled. In-flight
// attempts finish (bounded by Config.Timeout) before Run returns.
func (d *Dispatcher) Run(ctx context.Context) {
	sem := make(chan struct{}, d.cfg.Workers)
	var wg sync.WaitGroup
	defer wg.Wait()

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-d.wake:
		}
		d.drain(ctx, sem, &wg)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d.cfg.PollInterval)
	}
}

// drain claims and dispatches batches until nothing is due or ctx ends.
func (d *Dispatcher) drain(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	for ctx.Err() == nil {
		free := cap(sem) - len(sem)
		if free == 0 {
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}: // wait for a worker slot
				<-sem
			}
			continue
		}
		batch, err := d.repo.ClaimDueDeliveries(ctx, free)
		if err != nil {
			if ctx.Err() == nil {
				d.log.Error().Err(err).Msg("claim due deliveries")
			}
			return
		}
		if len(batch) == 0 {
			return
		}
		for i := range batch {
			dl := batch[i]
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				d.process(context.WithoutCancel(ctx), &dl, false)
			}()
		}
	}
}

// process makes one attempt for dl and persists the outcome. force = single
// attempt: a failure is final and inactive webhooks are still tried.
func (d *Dispatcher) process(ctx context.Context, dl *domain.WebhookDelivery, force bool) {
	w, err := d.repo.GetWebhook(ctx, dl.WebhookID)
	if err != nil {
		d.finish(ctx, dl, "failed", 0, fmt.Sprintf("webhook unavailable: %v", err))
		return
	}
	if !w.Active && !force {
		d.finish(ctx, dl, "failed", 0, "webhook is disabled")
		return
	}

	dl.Attempts++
	code, sendErr := d.send(ctx, w, dl)
	switch {
	case sendErr == nil:
		d.finish(ctx, dl, "delivered", code, "")
		d.recordResult(ctx, w.ID, code, true)
	default:
		status := "pending"
		if force || dl.Attempts >= d.cfg.MaxAttempts {
			status = "failed"
		}
		d.finish(ctx, dl, status, code, sendErr.Error())
		d.recordResult(ctx, w.ID, code, false)
	}
}

// finish stores the delivery state; a pending status schedules the next try.
func (d *Dispatcher) finish(ctx context.Context, dl *domain.WebhookDelivery, status string, code int, lastErr string) {
	now := d.now()
	dl.Status = status
	dl.ResponseCode = code
	dl.LastError = lastErr
	dl.UpdatedAt = now
	if status == "pending" {
		next := now.Add(d.backoff(dl.Attempts))
		dl.NextTryAt = &next
	} else {
		dl.NextTryAt = nil
	}
	if err := d.repo.UpdateDelivery(ctx, dl); err != nil {
		d.log.Error().Err(err).Str("delivery", dl.ID.String()).Msg("update delivery")
	}
}

// backoff is the wait after the attempts-th failed attempt.
func (d *Dispatcher) backoff(attempts int) time.Duration {
	i := attempts - 1
	if i < 0 {
		i = 0
	}
	if i >= len(d.cfg.Backoff) {
		i = len(d.cfg.Backoff) - 1
	}
	return d.cfg.Backoff[i]
}

const maxErrBody = 200

// send POSTs the delivery and returns the HTTP status (0 on transport error).
// A nil error means the endpoint answered 2xx.
func (d *Dispatcher) send(ctx context.Context, w *domain.Webhook, dl *domain.WebhookDelivery) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(dl.Payload))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	ts := strconv.FormatInt(d.now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CallGo-Webhooks/1.0")
	req.Header.Set(HeaderEvent, string(dl.EventType))
	req.Header.Set(HeaderDelivery, dl.ID.String())
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, Sign(w.Secret, ts, dl.Payload))

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	msg := fmt.Sprintf("http %d", resp.StatusCode)
	if s := string(bytes.TrimSpace(snippet)); s != "" {
		msg += ": " + s
	}
	return resp.StatusCode, errors.New(msg)
}

// recordResult updates the webhook's failure counter / last status and
// disables it after Config.MaxFailuresToDisable consecutive failures.
func (d *Dispatcher) recordResult(ctx context.Context, id uuid.UUID, code int, ok bool) {
	d.statsMu.Lock()
	w, err := d.repo.GetWebhook(ctx, id)
	if err != nil {
		d.statsMu.Unlock()
		d.log.Warn().Err(err).Str("webhook", id.String()).Msg("reload webhook for stats")
		return
	}
	now := d.now()
	w.LastStatus = code
	w.LastAt = &now
	disabled := false
	if ok {
		w.FailureCount = 0
	} else {
		w.FailureCount++
		if w.Active && w.FailureCount >= d.cfg.MaxFailuresToDisable {
			w.Active = false
			disabled = true
		}
	}
	w.UpdatedAt = now
	err = d.repo.UpdateWebhook(ctx, w)
	d.statsMu.Unlock()
	if err != nil {
		d.log.Error().Err(err).Str("webhook", id.String()).Msg("update webhook stats")
		return
	}
	if disabled {
		d.log.Warn().Str("webhook", id.String()).Str("url", w.URL).Int("failures", w.FailureCount).Msg("webhook disabled after repeated failures")
		if d.bus != nil {
			d.bus.Publish(ctx, domain.Event{
				ID: uuid.NewString(), Type: EventWebhookFailed, OrgID: w.OrgID, At: now,
				Payload: map[string]any{"webhookId": w.ID, "url": w.URL},
			})
		}
	}
}
