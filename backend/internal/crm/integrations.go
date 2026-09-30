package crm

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Compile-time proof that *Store satisfies the integrations port.
var _ domain.IntegrationsRepository = (*Store)(nil)

// Webhook delivery states (domain.WebhookDelivery.Status).
const (
	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// SMS states (domain.SMSMessage.Status).
const (
	SMSQueued = "queued"
	SMSSent   = "sent"
	SMSFailed = "failed"
)

// DeliveryClaimLease is how long a delivery claimed by ClaimDueDeliveries is
// hidden from other claimers. The worker is expected to UpdateDelivery with
// the outcome well before it expires; if it crashes, the delivery becomes due
// again once the lease lapses.
const DeliveryClaimLease = 2 * time.Minute

// Page sizes for integration listings.
const (
	defaultIntegrationLimit = 50
	maxIntegrationLimit     = 500
	maxClaimBatch           = 500
)

// eventWildcard subscribes a webhook to every event type.
const eventWildcard = "*"

// ---------------------------------------------------------------------------
// Webhooks
// ---------------------------------------------------------------------------

const webhookCols = `id, org_id, url, secret_enc, secret_hint, events, active, description, failure_count,
	last_status, last_at, created_at, updated_at`

// webhookSecretHint renders the last four characters of a signing secret.
func webhookSecretHint(secret string) string {
	r := []rune(secret)
	switch {
	case len(r) == 0:
		return ""
	case len(r) < 8:
		return "…"
	default:
		return "…" + string(r[len(r)-4:])
	}
}

// scanWebhookWith scans a webhook row; withSecret decrypts the signing
// secret, otherwise Secret is left blank.
func (s *Store) scanWebhookWith(withSecret bool) func(pgx.Row) (*domain.Webhook, error) {
	return func(row pgx.Row) (*domain.Webhook, error) {
		var w domain.Webhook
		var enc string
		err := row.Scan(&w.ID, &w.OrgID, &w.URL, &enc, &w.SecretHint, &w.Events, &w.Active, &w.Description,
			&w.FailureCount, &w.LastStatus, &w.LastAt, &w.CreatedAt, &w.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if w.Events == nil {
			w.Events = []string{}
		}
		if withSecret {
			if w.Secret, err = s.cipher.decrypt(enc); err != nil {
				return nil, fmt.Errorf("webhook secret: %w", err)
			}
		}
		return &w, nil
	}
}

// CreateWebhook inserts an endpoint. The secret is encrypted at rest and
// w.SecretHint is filled in.
func (s *Store) CreateWebhook(ctx context.Context, w *domain.Webhook) error {
	enc, err := s.cipher.encrypt(w.Secret)
	if err != nil {
		return err
	}
	w.SecretHint = webhookSecretHint(w.Secret)
	w.Events = nonNilStrings(w.Events)
	row := s.db.QueryRow(ctx,
		`INSERT INTO webhooks (id, org_id, url, secret_enc, secret_hint, events, active, description,
			failure_count, last_status, last_at)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING id, created_at, updated_at`,
		nilIfZero(w.ID), w.OrgID, w.URL, enc, w.SecretHint, w.Events, w.Active, w.Description,
		w.FailureCount, w.LastStatus, w.LastAt)
	return dbErr("create webhook", row.Scan(&w.ID, &w.CreatedAt, &w.UpdatedAt))
}

// UpdateWebhook overwrites all mutable fields (including delivery health:
// FailureCount, LastStatus, LastAt). An empty Secret keeps the stored one;
// a non-empty Secret rotates it. SecretHint is refreshed from the database.
func (s *Store) UpdateWebhook(ctx context.Context, w *domain.Webhook) error {
	var enc, hint *string
	if w.Secret != "" {
		e, err := s.cipher.encrypt(w.Secret)
		if err != nil {
			return err
		}
		h := webhookSecretHint(w.Secret)
		enc, hint = &e, &h
	}
	w.Events = nonNilStrings(w.Events)
	row := s.db.QueryRow(ctx,
		`UPDATE webhooks SET url = $2, secret_enc = COALESCE($3, secret_enc),
			secret_hint = COALESCE($4, secret_hint), events = $5, active = $6, description = $7,
			failure_count = $8, last_status = $9, last_at = $10, updated_at = now()
		 WHERE id = $1
		 RETURNING org_id, secret_hint, created_at, updated_at`,
		w.ID, w.URL, enc, hint, w.Events, w.Active, w.Description, w.FailureCount, w.LastStatus, w.LastAt)
	return dbErr("update webhook", row.Scan(&w.OrgID, &w.SecretHint, &w.CreatedAt, &w.UpdatedAt))
}

// DeleteWebhook removes an endpoint and its delivery log.
func (s *Store) DeleteWebhook(ctx context.Context, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM webhooks WHERE id = $1`, id)
	return affected("delete webhook", tag, err)
}

// GetWebhook returns an endpoint including its decrypted signing secret.
func (s *Store) GetWebhook(ctx context.Context, id uuid.UUID) (*domain.Webhook, error) {
	w, err := s.scanWebhookWith(true)(s.db.QueryRow(ctx,
		`SELECT `+webhookCols+` FROM webhooks WHERE id = $1`, id))
	return w, dbErr("get webhook", err)
}

// ListWebhooks lists an organisation's endpoints, oldest first. Secret is
// blank; SecretHint shows its last four characters.
func (s *Store) ListWebhooks(ctx context.Context, orgID uuid.UUID) ([]domain.Webhook, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+webhookCols+` FROM webhooks WHERE org_id = $1 ORDER BY created_at, id`, orgID)
	if err != nil {
		return nil, dbErr("list webhooks", err)
	}
	return collect("list webhooks", rows, s.scanWebhookWith(false))
}

// ListActiveWebhooksForEvent returns the active endpoints of an organisation
// subscribed to ev (or to "*"), with their signing secrets so the dispatcher
// can sign payloads.
func (s *Store) ListActiveWebhooksForEvent(ctx context.Context, orgID uuid.UUID, ev domain.EventType) ([]domain.Webhook, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+webhookCols+` FROM webhooks
		 WHERE org_id = $1 AND active AND events && ARRAY[$2::text, $3::text]
		 ORDER BY created_at, id`,
		orgID, string(ev), eventWildcard)
	if err != nil {
		return nil, dbErr("list webhooks for event", err)
	}
	return collect("list webhooks for event", rows, s.scanWebhookWith(true))
}

// ---------------------------------------------------------------------------
// Webhook deliveries
// ---------------------------------------------------------------------------

const deliveryCols = `id, webhook_id, event_id, event_type, status, attempts, response_code, last_error,
	next_try_at, payload, created_at, updated_at`

func scanDelivery(row pgx.Row) (*domain.WebhookDelivery, error) {
	var d domain.WebhookDelivery
	err := row.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType, &d.Status, &d.Attempts, &d.ResponseCode,
		&d.LastError, &d.NextTryAt, &d.Payload, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// CreateDelivery queues a delivery. Status defaults to "pending"; a nil
// NextTryAt means "due now".
func (s *Store) CreateDelivery(ctx context.Context, d *domain.WebhookDelivery) error {
	if d.Status == "" {
		d.Status = DeliveryPending
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO webhook_deliveries (id, webhook_id, event_id, event_type, status, attempts, response_code,
			last_error, next_try_at, payload)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at, updated_at`,
		nilIfZero(d.ID), d.WebhookID, d.EventID, string(d.EventType), d.Status, d.Attempts, d.ResponseCode,
		d.LastError, d.NextTryAt, nonNilBytes(d.Payload))
	return dbErr("create delivery", row.Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt))
}

// UpdateDelivery records an attempt's outcome: status, attempts, response
// code, last error and next try time. Payload and identity are immutable.
func (s *Store) UpdateDelivery(ctx context.Context, d *domain.WebhookDelivery) error {
	row := s.db.QueryRow(ctx,
		`UPDATE webhook_deliveries SET status = $2, attempts = $3, response_code = $4, last_error = $5,
			next_try_at = $6, updated_at = now()
		 WHERE id = $1
		 RETURNING webhook_id, created_at, updated_at`,
		d.ID, d.Status, d.Attempts, d.ResponseCode, d.LastError, d.NextTryAt)
	return dbErr("update delivery", row.Scan(&d.WebhookID, &d.CreatedAt, &d.UpdatedAt))
}

// ListDeliveries returns one page of a webhook's delivery log (newest first)
// and the total count.
func (s *Store) ListDeliveries(ctx context.Context, webhookID uuid.UUID, limit, offset int) ([]domain.WebhookDelivery, int, error) {
	limit, offset = clampPage(limit, offset, defaultIntegrationLimit, maxIntegrationLimit)
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE webhook_id = $1`, webhookID).
		Scan(&total); err != nil {
		return nil, 0, dbErr("count deliveries", err)
	}
	if total == 0 || offset >= total {
		return []domain.WebhookDelivery{}, total, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+deliveryCols+` FROM webhook_deliveries WHERE webhook_id = $1
		 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, webhookID, limit, offset)
	if err != nil {
		return nil, 0, dbErr("list deliveries", err)
	}
	list, err := collect("list deliveries", rows, scanDelivery)
	return list, total, err
}

// ClaimDueDeliveries atomically claims up to n pending deliveries whose
// next_try_at is due (or NULL), oldest first. Rows locked by a concurrent
// claimer are skipped (FOR UPDATE SKIP LOCKED). Each claimed delivery has
// Attempts incremented and NextTryAt pushed DeliveryClaimLease into the
// future, so it is not handed out again while the worker is sending it; the
// worker then calls UpdateDelivery with the outcome.
func (s *Store) ClaimDueDeliveries(ctx context.Context, n int) ([]domain.WebhookDelivery, error) {
	if n <= 0 {
		return []domain.WebhookDelivery{}, nil
	}
	n = min(n, maxClaimBatch)
	rows, err := s.db.Query(ctx,
		`WITH due AS (
			SELECT id FROM webhook_deliveries
			WHERE status = 'pending' AND (next_try_at IS NULL OR next_try_at <= now())
			ORDER BY next_try_at NULLS FIRST, created_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		 )
		 UPDATE webhook_deliveries d
		 SET attempts = d.attempts + 1, next_try_at = now() + make_interval(secs => $2::float8), updated_at = now()
		 FROM due WHERE d.id = due.id
		 RETURNING `+prefixCols("d", deliveryCols),
		n, DeliveryClaimLease.Seconds())
	if err != nil {
		return nil, dbErr("claim deliveries", err)
	}
	list, err := collect("claim deliveries", rows, scanDelivery)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(a, b domain.WebhookDelivery) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return slices.Compare(a.ID[:], b.ID[:])
	})
	return list, nil
}

// ---------------------------------------------------------------------------
// SMS
// ---------------------------------------------------------------------------

const smsCols = `id, org_id, call_id, to_number, body, provider, provider_ref, status, error, created_at, sent_at`

func scanSMS(row pgx.Row) (*domain.SMSMessage, error) {
	var m domain.SMSMessage
	err := row.Scan(&m.ID, &m.OrgID, &m.CallID, &m.To, &m.Body, &m.Provider, &m.ProviderRef, &m.Status,
		&m.Error, &m.CreatedAt, &m.SentAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// CreateSMS records an outbound text. Status defaults to "queued".
func (s *Store) CreateSMS(ctx context.Context, m *domain.SMSMessage) error {
	if m.Status == "" {
		m.Status = SMSQueued
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO sms_messages (id, org_id, call_id, to_number, body, provider, provider_ref, status, error,
			sent_at)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING id, created_at`,
		nilIfZero(m.ID), m.OrgID, m.CallID, m.To, m.Body, m.Provider, m.ProviderRef, m.Status, m.Error, m.SentAt)
	return dbErr("create sms", row.Scan(&m.ID, &m.CreatedAt))
}

// UpdateSMS records the send result: provider, provider ref, status, error
// and sent time.
func (s *Store) UpdateSMS(ctx context.Context, m *domain.SMSMessage) error {
	row := s.db.QueryRow(ctx,
		`UPDATE sms_messages SET provider = $2, provider_ref = $3, status = $4, error = $5, sent_at = $6
		 WHERE id = $1
		 RETURNING org_id, created_at`,
		m.ID, m.Provider, m.ProviderRef, m.Status, m.Error, m.SentAt)
	return dbErr("update sms", row.Scan(&m.OrgID, &m.CreatedAt))
}

// ListSMS returns one page of an organisation's texts (newest first) and the
// total count.
func (s *Store) ListSMS(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]domain.SMSMessage, int, error) {
	limit, offset = clampPage(limit, offset, defaultIntegrationLimit, maxIntegrationLimit)
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM sms_messages WHERE org_id = $1`, orgID).
		Scan(&total); err != nil {
		return nil, 0, dbErr("count sms", err)
	}
	if total == 0 || offset >= total {
		return []domain.SMSMessage{}, total, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+smsCols+` FROM sms_messages WHERE org_id = $1
		 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`, orgID, limit, offset)
	if err != nil {
		return nil, 0, dbErr("list sms", err)
	}
	list, err := collect("list sms", rows, scanSMS)
	return list, total, err
}

// ---------------------------------------------------------------------------
// Callbacks
// ---------------------------------------------------------------------------

const callbackCols = `id, org_id, source_call_id, contact_id, phone, name, note, due_at, sip_number_id,
	agent_profile_id, status, result_call_id, attempts, created_by, created_at, updated_at`

func scanCallback(row pgx.Row) (*domain.CallbackRequest, error) {
	var c domain.CallbackRequest
	err := row.Scan(&c.ID, &c.OrgID, &c.SourceCallID, &c.ContactID, &c.Phone, &c.Name, &c.Note, &c.DueAt,
		&c.SIPNumberID, &c.AgentProfileID, &c.Status, &c.ResultCallID, &c.Attempts, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateCallback schedules a callback. Status defaults to "pending" and a
// zero DueAt to now().
func (s *Store) CreateCallback(ctx context.Context, c *domain.CallbackRequest) error {
	if c.Status == "" {
		c.Status = domain.CallbackPending
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO callback_requests (id, org_id, source_call_id, contact_id, phone, name, note, due_at,
			sip_number_id, agent_profile_id, status, result_call_id, attempts, created_by)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, COALESCE($8, now()), $9, $10, $11,
			$12, $13, $14)
		 RETURNING id, due_at, created_at, updated_at`,
		nilIfZero(c.ID), c.OrgID, c.SourceCallID, c.ContactID, c.Phone, c.Name, c.Note, nilIfZeroTime(c.DueAt),
		c.SIPNumberID, c.AgentProfileID, string(c.Status), c.ResultCallID, c.Attempts, c.CreatedBy)
	return dbErr("create callback", row.Scan(&c.ID, &c.DueAt, &c.CreatedAt, &c.UpdatedAt))
}

// UpdateCallback overwrites all mutable fields (org, source call and creator
// are immutable). A zero DueAt keeps the stored one.
func (s *Store) UpdateCallback(ctx context.Context, c *domain.CallbackRequest) error {
	row := s.db.QueryRow(ctx,
		`UPDATE callback_requests SET contact_id = $2, phone = $3, name = $4, note = $5,
			due_at = COALESCE($6, due_at), sip_number_id = $7, agent_profile_id = $8, status = $9,
			result_call_id = $10, attempts = $11, updated_at = now()
		 WHERE id = $1
		 RETURNING org_id, source_call_id, due_at, created_by, created_at, updated_at`,
		c.ID, c.ContactID, c.Phone, c.Name, c.Note, nilIfZeroTime(c.DueAt), c.SIPNumberID, c.AgentProfileID,
		string(c.Status), c.ResultCallID, c.Attempts)
	return dbErr("update callback", row.Scan(&c.OrgID, &c.SourceCallID, &c.DueAt, &c.CreatedBy,
		&c.CreatedAt, &c.UpdatedAt))
}

// GetCallback returns a callback by ID.
func (s *Store) GetCallback(ctx context.Context, id uuid.UUID) (*domain.CallbackRequest, error) {
	c, err := scanCallback(s.db.QueryRow(ctx, `SELECT `+callbackCols+` FROM callback_requests WHERE id = $1`, id))
	return c, dbErr("get callback", err)
}

// ListCallbacks returns one page of an organisation's callbacks and the total
// count. An empty status lists every state. Pending callbacks are ordered
// soonest-due first; other listings latest-due first.
func (s *Store) ListCallbacks(ctx context.Context, orgID uuid.UUID, status domain.CallbackStatus, limit, offset int) ([]domain.CallbackRequest, int, error) {
	limit, offset = clampPage(limit, offset, defaultIntegrationLimit, maxIntegrationLimit)
	const where = ` FROM callback_requests WHERE org_id = $1 AND ($2::text = '' OR status = $2::text)`
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*)`+where, orgID, string(status)).Scan(&total); err != nil {
		return nil, 0, dbErr("count callbacks", err)
	}
	if total == 0 || offset >= total {
		return []domain.CallbackRequest{}, total, nil
	}
	order := ` ORDER BY due_at DESC, id DESC`
	if status == domain.CallbackPending {
		order = ` ORDER BY due_at, id`
	}
	rows, err := s.db.Query(ctx, `SELECT `+callbackCols+where+order+` LIMIT $3 OFFSET $4`,
		orgID, string(status), limit, offset)
	if err != nil {
		return nil, 0, dbErr("list callbacks", err)
	}
	list, err := collect("list callbacks", rows, scanCallback)
	return list, total, err
}

// ClaimDueCallbacks atomically moves up to n pending callbacks whose due time
// has passed to "dialed" (incrementing Attempts), soonest-due first, and
// returns them. Concurrent claimers never receive the same callback (FOR
// UPDATE SKIP LOCKED + the status change).
func (s *Store) ClaimDueCallbacks(ctx context.Context, n int) ([]domain.CallbackRequest, error) {
	if n <= 0 {
		return []domain.CallbackRequest{}, nil
	}
	n = min(n, maxClaimBatch)
	rows, err := s.db.Query(ctx,
		`WITH due AS (
			SELECT id FROM callback_requests
			WHERE status = 'pending' AND due_at <= now()
			ORDER BY due_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		 )
		 UPDATE callback_requests c
		 SET status = 'dialed', attempts = c.attempts + 1, updated_at = now()
		 FROM due WHERE c.id = due.id
		 RETURNING `+prefixCols("c", callbackCols), n)
	if err != nil {
		return nil, dbErr("claim callbacks", err)
	}
	list, err := collect("claim callbacks", rows, scanCallback)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(a, b domain.CallbackRequest) int {
		if c := a.DueAt.Compare(b.DueAt); c != 0 {
			return c
		}
		return slices.Compare(a.ID[:], b.ID[:])
	})
	return list, nil
}

// ---------------------------------------------------------------------------
// Call recording / handoff helpers (not part of a domain port)
// ---------------------------------------------------------------------------

// ListCallsForRetention returns up to limit calls of any organisation whose
// recording is "ready" and that ended before `before`, oldest first. Used by
// the recording retention sweeper.
func (s *Store) ListCallsForRetention(ctx context.Context, before time.Time, limit int) ([]domain.Call, error) {
	limit, _ = clampPage(limit, 0, defaultIntegrationLimit, maxIntegrationLimit)
	rows, err := s.db.Query(ctx,
		`SELECT `+callCols+` FROM calls c
		 WHERE c.recording IS NOT NULL AND c.recording ->> 'status' = 'ready' AND c.ended_at < $1
		 ORDER BY c.ended_at, c.id
		 LIMIT $2`, before, limit)
	if err != nil {
		return nil, dbErr("list calls for retention", err)
	}
	return collect("list calls for retention", rows, scanCall)
}

// SetCallRecording replaces a call's recording info; nil clears it.
func (s *Store) SetCallRecording(ctx context.Context, callID uuid.UUID, rec *domain.RecordingInfo) error {
	enc, err := jsonNullable(rec)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE calls SET recording = $2::jsonb, updated_at = now() WHERE id = $1`, callID, enc)
	return affected("set call recording", tag, err)
}

// SetCallHandoff sets a call's operator-handoff state. A non-nil operatorID
// records the operator; nil keeps the stored one (so "ended" still shows who
// took the call), except that HandoffNone clears it.
func (s *Store) SetCallHandoff(ctx context.Context, callID uuid.UUID, state domain.HandoffState, operatorID *uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE calls SET handoff = $2::text,
			operator_id = CASE WHEN $2::text = '' THEN NULL ELSE COALESCE($3::uuid, operator_id) END,
			updated_at = now()
		 WHERE id = $1`, callID, string(state), operatorID)
	return affected("set call handoff", tag, err)
}
