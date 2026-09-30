package crm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Compile-time proof that *Store satisfies the billing port.
var _ domain.BillingRepository = (*Store)(nil)

// PlanLookup resolves a plan code to its limits and prices. SummarizeUsage
// uses it for IncludedMinutes / OverageMNTPerMin.
type PlanLookup func(code string) (domain.Plan, bool)

// SetPlanLookup installs the plan catalog used by SummarizeUsage (normally the
// billing service's catalog). nil restores DefaultPlanLookup. Call it during
// wiring, before the Store is shared between goroutines.
func (s *Store) SetPlanLookup(fn PlanLookup) { s.plans = fn }

func (s *Store) planLookup() PlanLookup {
	if s.plans != nil {
		return s.plans
	}
	return DefaultPlanLookup
}

// DefaultPlans returns the built-in plan catalog of docs/ROADMAP_SAAS.md
// (MNT, VAT excluded). Enterprise terms are negotiated: its zero included
// minutes / prices / limits are placeholders that Subscription.CustomLimits
// overrides (0 limits mean unlimited).
func DefaultPlans() []domain.Plan {
	return []domain.Plan{
		{
			Code: "trial", Name: "Туршилт (14 хоног)", MonthlyMNT: 0, IncludedMinutes: 100, OverageMNTPerMin: 0,
			MaxConcurrentCalls: 2, MaxAgentProfiles: 1, MaxUsers: 2, MaxKnowledgeMB: 10, MaxSIPNumbers: 1,
			Features: []string{"analytics", "recordings"}, TrialDays: 14, Public: true,
		},
		{
			Code: "starter", Name: "Starter", MonthlyMNT: 290_000, IncludedMinutes: 1_000, OverageMNTPerMin: 350,
			MaxConcurrentCalls: 3, MaxAgentProfiles: 3, MaxUsers: 5, MaxKnowledgeMB: 50, MaxSIPNumbers: 1,
			Features: []string{"analytics", "recordings", "webhooks", "sms", "api"}, Public: true,
		},
		{
			Code: "growth", Name: "Growth", MonthlyMNT: 890_000, IncludedMinutes: 4_000, OverageMNTPerMin: 300,
			MaxConcurrentCalls: 10, MaxAgentProfiles: 10, MaxUsers: 15, MaxKnowledgeMB: 500, MaxSIPNumbers: 3,
			Features: []string{"analytics", "recordings", "webhooks", "sms", "api", "handoff"}, Public: true,
		},
		{
			Code: "enterprise", Name: "Enterprise",
			Features: []string{"analytics", "recordings", "webhooks", "sms", "api", "handoff", "priority_support"},
			Public:   true,
		},
	}
}

// DefaultPlanLookup looks code up in DefaultPlans.
func DefaultPlanLookup(code string) (domain.Plan, bool) {
	for _, p := range DefaultPlans() {
		if p.Code == code {
			return p, true
		}
	}
	return domain.Plan{}, false
}

// jsonValue marshals v for a jsonb parameter.
func jsonValue(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("crm: marshal json: %w", err)
	}
	return string(b), nil
}

// ---------------------------------------------------------------------------
// Subscriptions
// ---------------------------------------------------------------------------

const subscriptionCols = `id, org_id, plan_code, status, current_period_start, current_period_end,
	trial_ends_at, canceled_at, custom_limits, created_at, updated_at`

func scanSubscription(row pgx.Row) (*domain.Subscription, error) {
	var (
		sub    domain.Subscription
		custom []byte
	)
	if err := row.Scan(&sub.ID, &sub.OrgID, &sub.PlanCode, &sub.Status, &sub.CurrentPeriodStart,
		&sub.CurrentPeriodEnd, &sub.TrialEndsAt, &sub.CanceledAt, &custom, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
		return nil, err
	}
	if len(custom) > 0 && string(custom) != "null" {
		var p domain.Plan
		if err := json.Unmarshal(custom, &p); err != nil {
			return nil, fmt.Errorf("decode custom limits: %w", err)
		}
		sub.CustomLimits = &p
	}
	return &sub, nil
}

// UpsertSubscription creates or replaces the (single) subscription of
// sub.OrgID and mirrors its plan into organizations.plan_code, atomically.
// Status defaults to trialing. sub is refreshed from the stored row (an
// existing subscription keeps its ID and CreatedAt).
func (s *Store) UpsertSubscription(ctx context.Context, sub *domain.Subscription) error {
	if sub.Status == "" {
		sub.Status = domain.SubTrialing
	}
	var custom *string
	if sub.CustomLimits != nil {
		js, err := jsonValue(sub.CustomLimits)
		if err != nil {
			return err
		}
		custom = &js
	}
	return s.inTx(ctx, func(tx *Store) error {
		got, err := scanSubscription(tx.db.QueryRow(ctx,
			`INSERT INTO subscriptions (id, org_id, plan_code, status, current_period_start, current_period_end,
				trial_ends_at, canceled_at, custom_limits)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9::jsonb)
			 ON CONFLICT (org_id) DO UPDATE SET
				plan_code = excluded.plan_code,
				status = excluded.status,
				current_period_start = excluded.current_period_start,
				current_period_end = excluded.current_period_end,
				trial_ends_at = excluded.trial_ends_at,
				canceled_at = excluded.canceled_at,
				custom_limits = excluded.custom_limits,
				updated_at = now()
			 RETURNING `+subscriptionCols,
			nilIfZero(sub.ID), sub.OrgID, sub.PlanCode, string(sub.Status), sub.CurrentPeriodStart,
			sub.CurrentPeriodEnd, sub.TrialEndsAt, sub.CanceledAt, custom))
		if err != nil {
			return dbErr("upsert subscription", err)
		}
		if _, err := tx.db.Exec(ctx,
			`UPDATE organizations SET plan_code = $2, updated_at = now() WHERE id = $1 AND plan_code <> $2`,
			sub.OrgID, sub.PlanCode); err != nil {
			return dbErr("upsert subscription: org plan", err)
		}
		*sub = *got
		return nil
	})
}

// GetSubscription returns an organisation's subscription.
func (s *Store) GetSubscription(ctx context.Context, orgID uuid.UUID) (*domain.Subscription, error) {
	sub, err := scanSubscription(s.db.QueryRow(ctx,
		`SELECT `+subscriptionCols+` FROM subscriptions WHERE org_id = $1`, orgID))
	return sub, dbErr("get subscription", err)
}

// ListSubscriptions lists subscriptions in status ("" = all), soonest period
// end first (the renewal / trial-expiry sweep order).
func (s *Store) ListSubscriptions(ctx context.Context, status domain.SubscriptionStatus) ([]domain.Subscription, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+subscriptionCols+` FROM subscriptions WHERE ($1 = '' OR status = $1)
		 ORDER BY current_period_end, id`, string(status))
	if err != nil {
		return nil, dbErr("list subscriptions", err)
	}
	return collect("list subscriptions", rows, scanSubscription)
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

const usageCols = `id, org_id, call_id, kind, quantity, cost_mnt, at`

func scanUsage(row pgx.Row) (*domain.UsageRecord, error) {
	var u domain.UsageRecord
	if err := row.Scan(&u.ID, &u.OrgID, &u.CallID, &u.Kind, &u.Quantity, &u.CostMNT, &u.At); err != nil {
		return nil, err
	}
	return &u, nil
}

// AddUsage bulk-inserts metered events in one statement (all or nothing).
// Zero IDs and At are filled in (a new UUID, now) and written back into recs.
func (s *Store) AddUsage(ctx context.Context, recs []domain.UsageRecord) error {
	if len(recs) == 0 {
		return nil
	}
	now := time.Now()
	rows := make([][]any, len(recs))
	for i := range recs {
		r := &recs[i]
		if r.ID == uuid.Nil {
			r.ID = uuid.New()
		}
		if r.At.IsZero() {
			r.At = now
		}
		rows[i] = []any{r.ID, r.OrgID, r.CallID, string(r.Kind), r.Quantity, r.CostMNT, r.At}
	}
	_, err := s.db.CopyFrom(ctx, pgx.Identifier{"usage_records"},
		[]string{"id", "org_id", "call_id", "kind", "quantity", "cost_mnt", "at"}, pgx.CopyFromRows(rows))
	return dbErr("add usage", err)
}

// SummarizeUsage aggregates an organisation's usage in [from, to). Calls is
// the number of distinct calls with call_minutes records, Minutes their sum,
// LLMTokens in+out tokens, CostMNT the internal cost of every record.
// IncludedMinutes / OverageMinutes / OverageMNT come from the subscription's
// plan (organizations.plan_code when there is no subscription) resolved via
// the PlanLookup; non-zero IncludedMinutes / OverageMNTPerMin in the
// subscription's CustomLimits override the plan. Overage is billed per started
// minute; a plan with OverageMNTPerMin 0 (blocked) reports overage minutes at
// 0 MNT. ErrNotFound when the organisation does not exist.
func (s *Store) SummarizeUsage(ctx context.Context, orgID uuid.UUID, from, to time.Time) (domain.UsageSummary, error) {
	sum := domain.UsageSummary{OrgID: orgID, PeriodStart: from, PeriodEnd: to}

	var (
		planCode string
		custom   []byte
	)
	if err := s.db.QueryRow(ctx,
		`SELECT COALESCE(s.plan_code, o.plan_code), s.custom_limits
		 FROM organizations o LEFT JOIN subscriptions s ON s.org_id = o.id
		 WHERE o.id = $1`, orgID).Scan(&planCode, &custom); err != nil {
		return sum, dbErr("summarize usage: plan", err)
	}

	var llmTokens, ttsChars, sms float64
	if err := s.db.QueryRow(ctx,
		`SELECT count(DISTINCT call_id) FILTER (WHERE kind = 'call_minutes'),
			COALESCE(sum(quantity) FILTER (WHERE kind = 'call_minutes'), 0),
			COALESCE(sum(quantity) FILTER (WHERE kind IN ('llm_tokens_in', 'llm_tokens_out')), 0),
			COALESCE(sum(quantity) FILTER (WHERE kind = 'stt_seconds'), 0),
			COALESCE(sum(quantity) FILTER (WHERE kind = 'tts_chars'), 0),
			COALESCE(sum(quantity) FILTER (WHERE kind = 'sms'), 0),
			COALESCE(sum(cost_mnt), 0)::bigint
		 FROM usage_records WHERE org_id = $1 AND at >= $2 AND at < $3`,
		orgID, from, to).Scan(&sum.Calls, &sum.Minutes, &llmTokens, &sum.STTSeconds, &ttsChars, &sms,
		&sum.CostMNT); err != nil {
		return sum, dbErr("summarize usage", err)
	}
	sum.LLMTokens = int64(math.Round(llmTokens))
	sum.TTSChars = int64(math.Round(ttsChars))
	sum.SMS = int(math.Round(sms))

	plan, _ := s.planLookup()(planCode)
	included, perMin := plan.IncludedMinutes, plan.OverageMNTPerMin
	if len(custom) > 0 && string(custom) != "null" {
		var c domain.Plan
		if err := json.Unmarshal(custom, &c); err != nil {
			return sum, fmt.Errorf("crm: summarize usage: decode custom limits: %w", err)
		}
		if c.IncludedMinutes > 0 {
			included = c.IncludedMinutes
		}
		if c.OverageMNTPerMin > 0 {
			perMin = c.OverageMNTPerMin
		}
	}
	sum.IncludedMinutes = included
	if over := sum.Minutes - float64(included); over > 0 {
		sum.OverageMinutes = over
		// Round away float noise before charging per started minute.
		billable := math.Ceil(math.Round(over*1e6) / 1e6)
		sum.OverageMNT = int64(billable) * perMin
	}
	return sum, nil
}

// ListUsage pages through an organisation's usage records in [from, to),
// newest first; kind "" = all kinds. Limit defaults to 100 (max 1000).
func (s *Store) ListUsage(ctx context.Context, orgID uuid.UUID, from, to time.Time, kind domain.UsageKind,
	limit, offset int) ([]domain.UsageRecord, int, error) {
	limit, offset = clampPage(limit, offset, 100, 1000)
	const where = `org_id = $1 AND at >= $2 AND at < $3 AND ($4 = '' OR kind = $4)`
	args := []any{orgID, from, to, string(kind)}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM usage_records WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, dbErr("list usage: count", err)
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+usageCols+` FROM usage_records WHERE `+where+` ORDER BY at DESC, id LIMIT $5 OFFSET $6`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, dbErr("list usage", err)
	}
	recs, err := collect("list usage", rows, scanUsage)
	if err != nil {
		return nil, 0, err
	}
	return recs, total, nil
}

// CountActiveCalls counts an organisation's calls that occupy a concurrency
// slot (queued, ringing or active).
func (s *Store) CountActiveCalls(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM calls WHERE org_id = $1 AND status IN ('queued', 'ringing', 'active')`, orgID).Scan(&n)
	return n, dbErr("count active calls", err)
}

// ---------------------------------------------------------------------------
// Invoices
// ---------------------------------------------------------------------------

const invoiceCols = `id, org_id, number, period_start, period_end, lines, subtotal_mnt, vat_mnt, total_mnt,
	status, due_at, paid_at, created_at`

func scanInvoice(row pgx.Row) (*domain.Invoice, error) {
	var inv domain.Invoice
	if err := row.Scan(&inv.ID, &inv.OrgID, &inv.Number, &inv.PeriodStart, &inv.PeriodEnd, &inv.Lines,
		&inv.SubtotalMNT, &inv.VATMNT, &inv.TotalMNT, &inv.Status, &inv.DueAt, &inv.PaidAt, &inv.CreatedAt); err != nil {
		return nil, err
	}
	if inv.Lines == nil {
		inv.Lines = []domain.InvoiceLine{}
	}
	return &inv, nil
}

func invoiceLines(lines []domain.InvoiceLine) (string, error) {
	if lines == nil {
		lines = []domain.InvoiceLine{}
	}
	return jsonValue(lines)
}

// CreateInvoice inserts an invoice and assigns its Number,
// "CG-<YYYY>-<NNNNNN>": the year of PeriodStart in the organisation's
// timezone and the next value of invoice_number_seq (gaps are possible when
// an insert fails). Status defaults to draft and a zero DueAt to PeriodEnd.
// A second invoice for the same org and PeriodStart is ErrConflict; an
// unknown org is ErrInvalid.
func (s *Store) CreateInvoice(ctx context.Context, inv *domain.Invoice) error {
	if inv.Status == "" {
		inv.Status = domain.InvoiceDraft
	}
	lines, err := invoiceLines(inv.Lines)
	if err != nil {
		return err
	}
	err = s.db.QueryRow(ctx,
		`WITH o AS (SELECT id, timezone FROM organizations WHERE id = $2::uuid),
		      n AS (SELECT nextval('invoice_number_seq') AS v FROM o)
		 INSERT INTO invoices (id, org_id, number, period_start, period_end, lines, subtotal_mnt, vat_mnt,
			total_mnt, status, due_at, paid_at)
		 SELECT COALESCE($1::uuid, gen_random_uuid()), o.id,
			'CG-' || to_char($3::timestamptz AT TIME ZONE o.timezone, 'YYYY') || '-' ||
				CASE WHEN n.v < 1000000 THEN lpad(n.v::text, 6, '0') ELSE n.v::text END,
			$3::timestamptz, $4::timestamptz, $5::jsonb, $6::bigint, $7::bigint, $8::bigint, $9::text,
			COALESCE($10::timestamptz, $4::timestamptz), $11::timestamptz
		 FROM o, n
		 RETURNING id, number, due_at, created_at`,
		nilIfZero(inv.ID), inv.OrgID, inv.PeriodStart, inv.PeriodEnd, lines, inv.SubtotalMNT, inv.VATMNT,
		inv.TotalMNT, string(inv.Status), nilIfZeroTime(inv.DueAt), inv.PaidAt).
		Scan(&inv.ID, &inv.Number, &inv.DueAt, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("crm: create invoice: unknown org %s: %w", inv.OrgID, domain.ErrInvalid)
	}
	if inv.Lines == nil {
		inv.Lines = []domain.InvoiceLine{}
	}
	return dbErr("create invoice", err)
}

// UpdateInvoice writes period_end, lines, amounts, status, due_at and paid_at
// of inv.ID. Number, org and period start are immutable.
func (s *Store) UpdateInvoice(ctx context.Context, inv *domain.Invoice) error {
	lines, err := invoiceLines(inv.Lines)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE invoices SET period_end = $2, lines = $3::jsonb, subtotal_mnt = $4, vat_mnt = $5, total_mnt = $6,
			status = $7, due_at = $8, paid_at = $9, updated_at = now()
		 WHERE id = $1`,
		inv.ID, inv.PeriodEnd, lines, inv.SubtotalMNT, inv.VATMNT, inv.TotalMNT, string(inv.Status), inv.DueAt,
		inv.PaidAt)
	return affected("update invoice", tag, err)
}

// GetInvoice returns an invoice by ID.
func (s *Store) GetInvoice(ctx context.Context, id uuid.UUID) (*domain.Invoice, error) {
	inv, err := scanInvoice(s.db.QueryRow(ctx, `SELECT `+invoiceCols+` FROM invoices WHERE id = $1`, id))
	return inv, dbErr("get invoice", err)
}

// ListInvoices lists an organisation's invoices, newest period first.
func (s *Store) ListInvoices(ctx context.Context, orgID uuid.UUID) ([]domain.Invoice, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+invoiceCols+` FROM invoices WHERE org_id = $1 ORDER BY period_start DESC, id`, orgID)
	if err != nil {
		return nil, dbErr("list invoices", err)
	}
	return collect("list invoices", rows, scanInvoice)
}

// GetInvoiceForPeriod returns the invoice of orgID whose period starts at
// periodStart (the invoicing job's idempotency check).
func (s *Store) GetInvoiceForPeriod(ctx context.Context, orgID uuid.UUID, periodStart time.Time) (*domain.Invoice, error) {
	inv, err := scanInvoice(s.db.QueryRow(ctx,
		`SELECT `+invoiceCols+` FROM invoices WHERE org_id = $1 AND period_start = $2`, orgID, periodStart))
	return inv, dbErr("get invoice for period", err)
}

// ---------------------------------------------------------------------------
// Payments
// ---------------------------------------------------------------------------

const paymentCols = `id, org_id, invoice_id, provider, provider_ref, amount_mnt, status, qr_text, qr_image,
	deep_links, expires_at, paid_at, raw, created_at`

func scanPayment(row pgx.Row) (*domain.Payment, error) {
	var p domain.Payment
	if err := row.Scan(&p.ID, &p.OrgID, &p.InvoiceID, &p.Provider, &p.ProviderRef, &p.AmountMNT, &p.Status,
		&p.QRText, &p.QRImage, &p.DeepLinks, &p.ExpiresAt, &p.PaidAt, &p.Raw, &p.CreatedAt); err != nil {
		return nil, err
	}
	if len(p.DeepLinks) == 0 {
		p.DeepLinks = nil
	}
	if len(p.Raw) == 0 {
		p.Raw = nil
	}
	return &p, nil
}

func paymentJSON(p *domain.Payment) (links, raw string, err error) {
	dl := p.DeepLinks
	if dl == nil {
		dl = []domain.PaymentLink{}
	}
	if links, err = jsonValue(dl); err != nil {
		return "", "", err
	}
	if raw, err = jsonObject(p.Raw); err != nil {
		return "", "", err
	}
	return links, raw, nil
}

// CreatePayment inserts a payment attempt. Status defaults to pending. A
// non-empty (Provider, ProviderRef) already in use is ErrConflict.
func (s *Store) CreatePayment(ctx context.Context, p *domain.Payment) error {
	if p.Status == "" {
		p.Status = domain.PaymentPending
	}
	links, raw, err := paymentJSON(p)
	if err != nil {
		return err
	}
	row := s.db.QueryRow(ctx,
		`INSERT INTO payments (id, org_id, invoice_id, provider, provider_ref, amount_mnt, status, qr_text,
			qr_image, deep_links, expires_at, paid_at, raw)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, $13::jsonb)
		 RETURNING id, created_at`,
		nilIfZero(p.ID), p.OrgID, p.InvoiceID, p.Provider, p.ProviderRef, p.AmountMNT, string(p.Status),
		p.QRText, p.QRImage, links, p.ExpiresAt, p.PaidAt, raw)
	return dbErr("create payment", row.Scan(&p.ID, &p.CreatedAt))
}

// UpdatePayment writes provider_ref, amount, status, QR data, deep links,
// expires_at, paid_at and raw of p.ID.
func (s *Store) UpdatePayment(ctx context.Context, p *domain.Payment) error {
	links, raw, err := paymentJSON(p)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE payments SET provider_ref = $2, amount_mnt = $3, status = $4, qr_text = $5, qr_image = $6,
			deep_links = $7::jsonb, expires_at = $8, paid_at = $9, raw = $10::jsonb, updated_at = now()
		 WHERE id = $1`,
		p.ID, p.ProviderRef, p.AmountMNT, string(p.Status), p.QRText, p.QRImage, links, p.ExpiresAt, p.PaidAt, raw)
	return affected("update payment", tag, err)
}

// GetPayment returns a payment by ID.
func (s *Store) GetPayment(ctx context.Context, id uuid.UUID) (*domain.Payment, error) {
	p, err := scanPayment(s.db.QueryRow(ctx, `SELECT `+paymentCols+` FROM payments WHERE id = $1`, id))
	return p, dbErr("get payment", err)
}

// GetPaymentByProviderRef resolves a provider callback to its payment. An
// empty ref never matches.
func (s *Store) GetPaymentByProviderRef(ctx context.Context, provider, ref string) (*domain.Payment, error) {
	p, err := scanPayment(s.db.QueryRow(ctx,
		`SELECT `+paymentCols+` FROM payments WHERE provider = $1 AND provider_ref = $2 AND provider_ref <> ''`,
		provider, ref))
	return p, dbErr("get payment by provider ref", err)
}

// ListPayments lists an invoice's payment attempts, oldest first.
func (s *Store) ListPayments(ctx context.Context, invoiceID uuid.UUID) ([]domain.Payment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+paymentCols+` FROM payments WHERE invoice_id = $1 ORDER BY created_at, id`, invoiceID)
	if err != nil {
		return nil, dbErr("list payments", err)
	}
	return collect("list payments", rows, scanPayment)
}
