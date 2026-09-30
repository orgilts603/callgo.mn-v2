// Package webhookstest provides an in-memory domain.IntegrationsRepository
// for tests of the integrations packages and their HTTP handlers.
package webhookstest

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// MemRepo is a concurrency-safe in-memory IntegrationsRepository.
type MemRepo struct {
	// Now is the clock used for due checks (default time.Now).
	Now func() time.Time
	// Lease is how far ClaimDueDeliveries pushes NextTryAt of claimed rows
	// (default 5 minutes), like a visibility timeout.
	Lease time.Duration

	mu         sync.Mutex
	webhooks   map[uuid.UUID]domain.Webhook
	deliveries map[uuid.UUID]domain.WebhookDelivery
	delOrder   []uuid.UUID
	sms        []domain.SMSMessage
	callbacks  map[uuid.UUID]domain.CallbackRequest
	cbOrder    []uuid.UUID

	// UpdateWebhookErr / CreateSMSErr inject failures when non-nil.
	UpdateWebhookErr error
	CreateSMSErr     error
}

var _ domain.IntegrationsRepository = (*MemRepo)(nil)

// New returns an empty repository.
func New() *MemRepo {
	return &MemRepo{
		webhooks:   map[uuid.UUID]domain.Webhook{},
		deliveries: map[uuid.UUID]domain.WebhookDelivery{},
		callbacks:  map[uuid.UUID]domain.CallbackRequest{},
	}
}

func (m *MemRepo) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *MemRepo) CreateWebhook(_ context.Context, w *domain.Webhook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = m.now()
	}
	w.UpdatedAt = w.CreatedAt
	m.webhooks[w.ID] = cloneWebhook(*w)
	return nil
}

func (m *MemRepo) UpdateWebhook(_ context.Context, w *domain.Webhook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateWebhookErr != nil {
		return m.UpdateWebhookErr
	}
	if _, ok := m.webhooks[w.ID]; !ok {
		return domain.ErrNotFound
	}
	m.webhooks[w.ID] = cloneWebhook(*w)
	return nil
}

func (m *MemRepo) DeleteWebhook(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.webhooks[id]; !ok {
		return domain.ErrNotFound
	}
	delete(m.webhooks, id)
	return nil
}

func (m *MemRepo) GetWebhook(_ context.Context, id uuid.UUID) (*domain.Webhook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.webhooks[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := cloneWebhook(w)
	return &c, nil
}

func (m *MemRepo) ListWebhooks(_ context.Context, orgID uuid.UUID) ([]domain.Webhook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Webhook
	for _, w := range m.webhooks {
		if w.OrgID == orgID {
			w.Secret = "" // list responses never carry secrets
			out = append(out, cloneWebhook(w))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemRepo) ListActiveWebhooksForEvent(_ context.Context, orgID uuid.UUID, ev domain.EventType) ([]domain.Webhook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Webhook
	for _, w := range m.webhooks {
		if w.OrgID == orgID && w.Active && (slices.Contains(w.Events, "*") || slices.Contains(w.Events, string(ev))) {
			out = append(out, cloneWebhook(w))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemRepo) CreateDelivery(_ context.Context, d *domain.WebhookDelivery) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}
	m.deliveries[d.ID] = cloneDelivery(*d)
	m.delOrder = append(m.delOrder, d.ID)
	return nil
}

func (m *MemRepo) UpdateDelivery(_ context.Context, d *domain.WebhookDelivery) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.deliveries[d.ID]; !ok {
		return domain.ErrNotFound
	}
	m.deliveries[d.ID] = cloneDelivery(*d)
	return nil
}

func (m *MemRepo) ListDeliveries(_ context.Context, webhookID uuid.UUID, limit, offset int) ([]domain.WebhookDelivery, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []domain.WebhookDelivery
	for i := len(m.delOrder) - 1; i >= 0; i-- { // newest first
		if d := m.deliveries[m.delOrder[i]]; d.WebhookID == webhookID {
			all = append(all, cloneDelivery(d))
		}
	}
	total := len(all)
	if offset > total {
		offset = total
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, total, nil
}

// ClaimDueDeliveries returns up to n pending deliveries whose NextTryAt is
// due and leases them by pushing NextTryAt forward.
func (m *MemRepo) ClaimDueDeliveries(_ context.Context, n int) ([]domain.WebhookDelivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	lease := m.Lease
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	var out []domain.WebhookDelivery
	for _, id := range m.delOrder {
		if len(out) >= n {
			break
		}
		d := m.deliveries[id]
		if d.Status != "pending" || d.NextTryAt == nil || d.NextTryAt.After(now) {
			continue
		}
		out = append(out, cloneDelivery(d))
		leased := now.Add(lease)
		d.NextTryAt = &leased
		m.deliveries[id] = d
	}
	return out, nil
}

// Deliveries returns every stored delivery (oldest first) for assertions.
func (m *MemRepo) Deliveries() []domain.WebhookDelivery {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.WebhookDelivery, 0, len(m.delOrder))
	for _, id := range m.delOrder {
		out = append(out, cloneDelivery(m.deliveries[id]))
	}
	return out
}

func (m *MemRepo) CreateSMS(_ context.Context, s *domain.SMSMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateSMSErr != nil {
		return m.CreateSMSErr
	}
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	m.sms = append(m.sms, *s)
	return nil
}

func (m *MemRepo) UpdateSMS(_ context.Context, s *domain.SMSMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sms {
		if m.sms[i].ID == s.ID {
			m.sms[i] = *s
			return nil
		}
	}
	return domain.ErrNotFound
}

func (m *MemRepo) ListSMS(_ context.Context, orgID uuid.UUID, limit, offset int) ([]domain.SMSMessage, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []domain.SMSMessage
	for i := len(m.sms) - 1; i >= 0; i-- {
		if m.sms[i].OrgID == orgID {
			all = append(all, m.sms[i])
		}
	}
	total := len(all)
	if offset > total {
		offset = total
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, total, nil
}

// SMSMessages returns every stored SMS (oldest first) for assertions.
func (m *MemRepo) SMSMessages() []domain.SMSMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sms)
}

func (m *MemRepo) CreateCallback(_ context.Context, c *domain.CallbackRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	m.callbacks[c.ID] = *c
	m.cbOrder = append(m.cbOrder, c.ID)
	return nil
}

func (m *MemRepo) UpdateCallback(_ context.Context, c *domain.CallbackRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.callbacks[c.ID]; !ok {
		return domain.ErrNotFound
	}
	m.callbacks[c.ID] = *c
	return nil
}

func (m *MemRepo) GetCallback(_ context.Context, id uuid.UUID) (*domain.CallbackRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.callbacks[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &c, nil
}

func (m *MemRepo) ListCallbacks(_ context.Context, orgID uuid.UUID, status domain.CallbackStatus, limit, offset int) ([]domain.CallbackRequest, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []domain.CallbackRequest
	for _, id := range m.cbOrder {
		c := m.callbacks[id]
		if c.OrgID == orgID && (status == "" || c.Status == status) {
			all = append(all, c)
		}
	}
	total := len(all)
	if offset > total {
		offset = total
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, total, nil
}

func (m *MemRepo) ClaimDueCallbacks(_ context.Context, n int) ([]domain.CallbackRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var out []domain.CallbackRequest
	for _, id := range m.cbOrder {
		if len(out) >= n {
			break
		}
		c := m.callbacks[id]
		if c.Status == domain.CallbackPending && !c.DueAt.After(now) {
			c.Status = domain.CallbackDialed
			m.callbacks[id] = c
			out = append(out, c)
		}
	}
	return out, nil
}

func cloneWebhook(w domain.Webhook) domain.Webhook {
	w.Events = slices.Clone(w.Events)
	return w
}

func cloneDelivery(d domain.WebhookDelivery) domain.WebhookDelivery {
	d.Payload = slices.Clone(d.Payload)
	if d.NextTryAt != nil {
		t := *d.NextTryAt
		d.NextTryAt = &t
	}
	return d
}

// String helps debugging failing assertions.
func (m *MemRepo) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("MemRepo{webhooks:%d deliveries:%d sms:%d callbacks:%d}", len(m.webhooks), len(m.deliveries), len(m.sms), len(m.callbacks))
}
