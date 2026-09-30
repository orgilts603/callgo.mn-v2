package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ===========================================================================
// Identity: invitations, password resets, refresh sessions, API keys, audit
// ===========================================================================

// Invitation lets an admin add a user by email.
type Invitation struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"orgId"`
	Email      string     `json:"email"`
	Role       Role       `json:"role"`
	TokenHash  string     `json:"-"`
	InvitedBy  *uuid.UUID `json:"invitedBy,omitempty"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	AcceptedAt *time.Time `json:"acceptedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// PasswordReset is a one-time reset token.
type PasswordReset struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"userId"`
	TokenHash string     `json:"-"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// EmailVerification is a one-time email verification token.
type EmailVerification struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"userId"`
	TokenHash string     `json:"-"`
	ExpiresAt time.Time  `json:"expiresAt"`
	UsedAt    *time.Time `json:"usedAt,omitempty"`
}

// RefreshSession is a long-lived login session (refresh token, rotated on use).
type RefreshSession struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"userId"`
	OrgID     uuid.UUID  `json:"orgId"`
	TokenHash string     `json:"-"`
	UserAgent string     `json:"userAgent"`
	IP        string     `json:"ip"`
	ExpiresAt time.Time  `json:"expiresAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	LastUsed  time.Time  `json:"lastUsedAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

// APIKey authenticates server-to-server calls on behalf of an org.
// The plaintext key is shown once: "cg_live_<prefix>_<secret>".
type APIKey struct {
	ID         uuid.UUID  `json:"id"`
	OrgID      uuid.UUID  `json:"orgId"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"` // first 8 chars, for lookup + display
	KeyHash    string     `json:"-"`
	Scopes     []string   `json:"scopes"` // "calls:read", "calls:write", "campaigns:write", "contacts:write", "knowledge:write", "*"
	CreatedBy  *uuid.UUID `json:"createdBy,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// AuditEntry records who did what.
type AuditEntry struct {
	ID         uuid.UUID      `json:"id"`
	OrgID      uuid.UUID      `json:"orgId"`
	ActorID    *uuid.UUID     `json:"actorId,omitempty"`
	ActorEmail string         `json:"actorEmail"`
	Action     string         `json:"action"` // "user.invite", "llm_config.update", "campaign.start", ...
	TargetType string         `json:"targetType"`
	TargetID   string         `json:"targetId"`
	Meta       map[string]any `json:"meta,omitempty"`
	IP         string         `json:"ip"`
	At         time.Time      `json:"at"`
}

// IdentityRepository persists the identity entities. Tokens are stored hashed
// (sha256); lookups take the hash.
type IdentityRepository interface {
	CreateOrg(ctx context.Context, o *Organization) error
	UpdateOrg(ctx context.Context, o *Organization) error
	ListOrgs(ctx context.Context, search string, limit, offset int) ([]Organization, int, error)
	UpdateUser(ctx context.Context, u *User) error
	CountUsers(ctx context.Context, orgID uuid.UUID) (int, error)

	CreateInvitation(ctx context.Context, inv *Invitation) error
	GetInvitationByHash(ctx context.Context, tokenHash string) (*Invitation, error)
	ListInvitations(ctx context.Context, orgID uuid.UUID) ([]Invitation, error)
	DeleteInvitation(ctx context.Context, id uuid.UUID) error
	MarkInvitationAccepted(ctx context.Context, id uuid.UUID) error

	CreatePasswordReset(ctx context.Context, r *PasswordReset) error
	GetPasswordResetByHash(ctx context.Context, tokenHash string) (*PasswordReset, error)
	MarkPasswordResetUsed(ctx context.Context, id uuid.UUID) error

	CreateEmailVerification(ctx context.Context, v *EmailVerification) error
	GetEmailVerificationByHash(ctx context.Context, tokenHash string) (*EmailVerification, error)
	MarkEmailVerificationUsed(ctx context.Context, id uuid.UUID) error

	CreateRefreshSession(ctx context.Context, s *RefreshSession) error
	GetRefreshSessionByHash(ctx context.Context, tokenHash string) (*RefreshSession, error)
	RotateRefreshSession(ctx context.Context, id uuid.UUID, newHash string, expiresAt time.Time) error
	RevokeRefreshSession(ctx context.Context, id uuid.UUID) error
	RevokeUserSessions(ctx context.Context, userID uuid.UUID) error
	ListRefreshSessions(ctx context.Context, userID uuid.UUID) ([]RefreshSession, error)

	CreateAPIKey(ctx context.Context, k *APIKey) error
	GetAPIKeyByPrefix(ctx context.Context, prefix string) (*APIKey, error)
	ListAPIKeys(ctx context.Context, orgID uuid.UUID) ([]APIKey, error)
	RevokeAPIKey(ctx context.Context, id uuid.UUID) error
	TouchAPIKey(ctx context.Context, id uuid.UUID) error

	AppendAudit(ctx context.Context, e *AuditEntry) error
	ListAudit(ctx context.Context, orgID uuid.UUID, f AuditFilter) ([]AuditEntry, int, error)
}

// AuditFilter narrows an audit listing.
type AuditFilter struct {
	ActorID *uuid.UUID
	Action  string
	From    *time.Time
	To      *time.Time
	Limit   int
	Offset  int
}

// Mailer sends transactional email (driven port; SMTP or log implementation).
type Mailer interface {
	Send(ctx context.Context, to, subject, textBody, htmlBody string) error
}

// ===========================================================================
// Billing: plans, subscriptions, usage, invoices, payments
// ===========================================================================

// Plan is a price tier. Amounts are MNT, VAT excluded.
type Plan struct {
	Code               string   `json:"code"` // trial | starter | growth | enterprise
	Name               string   `json:"name"`
	MonthlyMNT         int64    `json:"monthlyMnt"`
	IncludedMinutes    int      `json:"includedMinutes"`
	OverageMNTPerMin   int64    `json:"overageMntPerMin"` // 0 = overage blocked
	MaxConcurrentCalls int      `json:"maxConcurrentCalls"`
	MaxAgentProfiles   int      `json:"maxAgentProfiles"` // 0 = unlimited
	MaxUsers           int      `json:"maxUsers"`
	MaxKnowledgeMB     int      `json:"maxKnowledgeMb"`
	MaxSIPNumbers      int      `json:"maxSipNumbers"`
	Features           []string `json:"features"` // "recordings", "webhooks", "sms", "analytics", "api", "handoff", "priority_support"
	TrialDays          int      `json:"trialDays"`
	Public             bool     `json:"public"`
}

// SubscriptionStatus is the billing state of an org.
type SubscriptionStatus string

const (
	SubTrialing SubscriptionStatus = "trialing"
	SubActive   SubscriptionStatus = "active"
	SubPastDue  SubscriptionStatus = "past_due"
	SubCanceled SubscriptionStatus = "canceled"
)

// Subscription binds an org to a plan for a billing period.
type Subscription struct {
	ID                 uuid.UUID          `json:"id"`
	OrgID              uuid.UUID          `json:"orgId"`
	PlanCode           string             `json:"planCode"`
	Status             SubscriptionStatus `json:"status"`
	CurrentPeriodStart time.Time          `json:"currentPeriodStart"`
	CurrentPeriodEnd   time.Time          `json:"currentPeriodEnd"`
	TrialEndsAt        *time.Time         `json:"trialEndsAt,omitempty"`
	CanceledAt         *time.Time         `json:"canceledAt,omitempty"`
	// Overrides for enterprise deals (nil = plan default).
	CustomLimits *Plan     `json:"customLimits,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// UsageKind enumerates metered quantities.
type UsageKind string

const (
	UsageCallMinutes  UsageKind = "call_minutes" // billable, rounded up per call
	UsageLLMTokensIn  UsageKind = "llm_tokens_in"
	UsageLLMTokensOut UsageKind = "llm_tokens_out"
	UsageSTTSeconds   UsageKind = "stt_seconds"
	UsageTTSChars     UsageKind = "tts_chars"
	UsageSMS          UsageKind = "sms"
)

// UsageRecord is one metered event.
type UsageRecord struct {
	ID       uuid.UUID  `json:"id"`
	OrgID    uuid.UUID  `json:"orgId"`
	CallID   *uuid.UUID `json:"callId,omitempty"`
	Kind     UsageKind  `json:"kind"`
	Quantity float64    `json:"quantity"`
	CostMNT  int64      `json:"costMnt"` // internal cost estimate (provider prices)
	At       time.Time  `json:"at"`
}

// CallUsage is the per-call breakdown reported by the agent worker.
type CallUsage struct {
	LLMTokensIn  int     `json:"llmTokensIn"`
	LLMTokensOut int     `json:"llmTokensOut"`
	STTSeconds   float64 `json:"sttSeconds"`
	TTSChars     int     `json:"ttsChars"`
	LLMModel     string  `json:"llmModel,omitempty"`
	CostMNT      int64   `json:"costMnt"`
}

// UsageSummary aggregates a billing period.
type UsageSummary struct {
	OrgID           uuid.UUID `json:"orgId"`
	PeriodStart     time.Time `json:"periodStart"`
	PeriodEnd       time.Time `json:"periodEnd"`
	Calls           int       `json:"calls"`
	Minutes         float64   `json:"minutes"`
	IncludedMinutes int       `json:"includedMinutes"`
	OverageMinutes  float64   `json:"overageMinutes"`
	OverageMNT      int64     `json:"overageMnt"`
	LLMTokens       int64     `json:"llmTokens"`
	STTSeconds      float64   `json:"sttSeconds"`
	TTSChars        int64     `json:"ttsChars"`
	SMS             int       `json:"sms"`
	CostMNT         int64     `json:"costMnt"`
}

// InvoiceStatus is the lifecycle of an invoice.
type InvoiceStatus string

const (
	InvoiceDraft InvoiceStatus = "draft"
	InvoiceOpen  InvoiceStatus = "open"
	InvoicePaid  InvoiceStatus = "paid"
	InvoiceVoid  InvoiceStatus = "void"
)

// InvoiceLine is one billed item.
type InvoiceLine struct {
	Description string  `json:"description"`
	Quantity    float64 `json:"quantity"`
	UnitMNT     int64   `json:"unitMnt"`
	AmountMNT   int64   `json:"amountMnt"`
}

// Invoice is a monthly bill (VAT 10%).
type Invoice struct {
	ID          uuid.UUID     `json:"id"`
	OrgID       uuid.UUID     `json:"orgId"`
	Number      string        `json:"number"` // CG-2026-000123
	PeriodStart time.Time     `json:"periodStart"`
	PeriodEnd   time.Time     `json:"periodEnd"`
	Lines       []InvoiceLine `json:"lines"`
	SubtotalMNT int64         `json:"subtotalMnt"`
	VATMNT      int64         `json:"vatMnt"`
	TotalMNT    int64         `json:"totalMnt"`
	Status      InvoiceStatus `json:"status"`
	DueAt       time.Time     `json:"dueAt"`
	PaidAt      *time.Time    `json:"paidAt,omitempty"`
	CreatedAt   time.Time     `json:"createdAt"`
}

// PaymentStatus is the state of a payment attempt.
type PaymentStatus string

const (
	PaymentPending PaymentStatus = "pending"
	PaymentPaid    PaymentStatus = "paid"
	PaymentFailed  PaymentStatus = "failed"
	PaymentExpired PaymentStatus = "expired"
)

// Payment is one attempt to settle an invoice through a provider.
type Payment struct {
	ID          uuid.UUID      `json:"id"`
	OrgID       uuid.UUID      `json:"orgId"`
	InvoiceID   uuid.UUID      `json:"invoiceId"`
	Provider    string         `json:"provider"`    // "qpay" | "mock" | "bank_transfer"
	ProviderRef string         `json:"providerRef"` // qpay invoice_id
	AmountMNT   int64          `json:"amountMnt"`
	Status      PaymentStatus  `json:"status"`
	QRText      string         `json:"qrText,omitempty"`
	QRImage     string         `json:"qrImage,omitempty"` // base64 PNG from provider
	DeepLinks   []PaymentLink  `json:"deepLinks,omitempty"`
	ExpiresAt   *time.Time     `json:"expiresAt,omitempty"`
	PaidAt      *time.Time     `json:"paidAt,omitempty"`
	Raw         map[string]any `json:"-"`
	CreatedAt   time.Time      `json:"createdAt"`
}

// PaymentLink is a bank-app deep link returned by QPay.
type PaymentLink struct {
	Name string `json:"name"`
	Logo string `json:"logo"`
	Link string `json:"link"`
}

// BillingRepository persists subscriptions, usage, invoices and payments.
type BillingRepository interface {
	UpsertSubscription(ctx context.Context, s *Subscription) error
	GetSubscription(ctx context.Context, orgID uuid.UUID) (*Subscription, error)
	ListSubscriptions(ctx context.Context, status SubscriptionStatus) ([]Subscription, error)

	AddUsage(ctx context.Context, recs []UsageRecord) error
	SummarizeUsage(ctx context.Context, orgID uuid.UUID, from, to time.Time) (UsageSummary, error)
	ListUsage(ctx context.Context, orgID uuid.UUID, from, to time.Time, kind UsageKind, limit, offset int) ([]UsageRecord, int, error)
	CountActiveCalls(ctx context.Context, orgID uuid.UUID) (int, error)

	CreateInvoice(ctx context.Context, inv *Invoice) error // assigns Number
	UpdateInvoice(ctx context.Context, inv *Invoice) error
	GetInvoice(ctx context.Context, id uuid.UUID) (*Invoice, error)
	ListInvoices(ctx context.Context, orgID uuid.UUID) ([]Invoice, error)
	GetInvoiceForPeriod(ctx context.Context, orgID uuid.UUID, periodStart time.Time) (*Invoice, error)

	CreatePayment(ctx context.Context, p *Payment) error
	UpdatePayment(ctx context.Context, p *Payment) error
	GetPayment(ctx context.Context, id uuid.UUID) (*Payment, error)
	GetPaymentByProviderRef(ctx context.Context, provider, ref string) (*Payment, error)
	ListPayments(ctx context.Context, invoiceID uuid.UUID) ([]Payment, error)
}

// PaymentProvider creates and checks payments (QPay behind the port).
type PaymentProvider interface {
	Name() string
	// CreateInvoice registers the payment with the provider and fills
	// ProviderRef, QRText, QRImage, DeepLinks, ExpiresAt.
	CreateInvoice(ctx context.Context, p *Payment, description string) error
	// Check queries the provider; returns the current status.
	Check(ctx context.Context, p *Payment) (PaymentStatus, error)
	// VerifyCallback parses a provider callback and returns the provider ref.
	VerifyCallback(ctx context.Context, query map[string]string, body []byte) (providerRef string, err error)
}

// ===========================================================================
// Recordings & operator handoff
// ===========================================================================

// RecordingInfo describes a stored call recording.
type RecordingInfo struct {
	EgressID    string     `json:"egressId,omitempty"`
	ObjectKey   string     `json:"objectKey,omitempty"` // path in the object store
	SizeBytes   int64      `json:"sizeBytes"`
	DurationSec int        `json:"durationSec"`
	Status      string     `json:"status"` // "recording" | "ready" | "failed" | "deleted"
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	EndedAt     *time.Time `json:"endedAt,omitempty"`
}

// ObjectStore stores recordings and exports (S3 / MinIO / local disk).
type ObjectStore interface {
	Put(ctx context.Context, key, contentType string, body []byte) error
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, key string) error
	Stat(ctx context.Context, key string) (size int64, exists bool, err error)
}

// HandoffState tracks whether a human operator is on the call.
type HandoffState string

const (
	HandoffNone      HandoffState = ""
	HandoffRequested HandoffState = "requested" // customer asked / AI escalated
	HandoffActive    HandoffState = "active"    // operator joined the room
	HandoffEnded     HandoffState = "ended"
)

// ===========================================================================
// Integrations: webhooks, SMS, post-call actions, callbacks
// ===========================================================================

// Webhook is an org-level outbound endpoint.
type Webhook struct {
	ID           uuid.UUID  `json:"id"`
	OrgID        uuid.UUID  `json:"orgId"`
	URL          string     `json:"url"`
	Secret       string     `json:"-"` // HMAC-SHA256 key; shown once
	SecretHint   string     `json:"secretHint"`
	Events       []string   `json:"events"` // EventType values or "*"
	Active       bool       `json:"active"`
	Description  string     `json:"description"`
	FailureCount int        `json:"failureCount"`
	LastStatus   int        `json:"lastStatus"`
	LastAt       *time.Time `json:"lastAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// WebhookDelivery is one attempt log.
type WebhookDelivery struct {
	ID           uuid.UUID  `json:"id"`
	WebhookID    uuid.UUID  `json:"webhookId"`
	EventID      string     `json:"eventId"`
	EventType    EventType  `json:"eventType"`
	Status       string     `json:"status"` // pending | delivered | failed
	Attempts     int        `json:"attempts"`
	ResponseCode int        `json:"responseCode"`
	LastError    string     `json:"lastError,omitempty"`
	NextTryAt    *time.Time `json:"nextTryAt,omitempty"`
	Payload      []byte     `json:"-"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// SMSMessage is an outbound text.
type SMSMessage struct {
	ID          uuid.UUID  `json:"id"`
	OrgID       uuid.UUID  `json:"orgId"`
	CallID      *uuid.UUID `json:"callId,omitempty"`
	To          string     `json:"to"`
	Body        string     `json:"body"`
	Provider    string     `json:"provider"`
	ProviderRef string     `json:"providerRef,omitempty"`
	Status      string     `json:"status"` // queued | sent | failed
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	SentAt      *time.Time `json:"sentAt,omitempty"`
}

// SMSSender delivers text messages (driven port; gateway or mock).
type SMSSender interface {
	Name() string
	Send(ctx context.Context, to, body string) (providerRef string, err error)
}

// PostCallActionType enumerates automations.
type PostCallActionType string

const (
	ActionSMS      PostCallActionType = "sms"
	ActionWebhook  PostCallActionType = "webhook"
	ActionCallback PostCallActionType = "callback"
)

// PostCallAction runs after a call ends when its condition matches.
// Template placeholders: {{name}}, {{phone}}, {{summary}}, {{outcome}}, {{campaign}}, {{vars.X}}.
type PostCallAction struct {
	Type      PostCallActionType `json:"type"`
	Outcomes  []string           `json:"outcomes"` // empty = always
	Template  string             `json:"template"` // SMS body
	WebhookID *uuid.UUID         `json:"webhookId,omitempty"`
	DelayMin  int                `json:"delayMin"` // callback: minutes after end (0 = use LLM-requested time)
}

// CallbackStatus is the state of a scheduled callback.
type CallbackStatus string

const (
	CallbackPending  CallbackStatus = "pending"
	CallbackDialed   CallbackStatus = "dialed"
	CallbackDone     CallbackStatus = "done"
	CallbackCanceled CallbackStatus = "canceled"
	CallbackFailed   CallbackStatus = "failed"
)

// CallbackRequest is a promise to call the customer back.
type CallbackRequest struct {
	ID             uuid.UUID      `json:"id"`
	OrgID          uuid.UUID      `json:"orgId"`
	SourceCallID   *uuid.UUID     `json:"sourceCallId,omitempty"`
	ContactID      *uuid.UUID     `json:"contactId,omitempty"`
	Phone          string         `json:"phone"`
	Name           string         `json:"name"`
	Note           string         `json:"note"`
	DueAt          time.Time      `json:"dueAt"`
	SIPNumberID    *uuid.UUID     `json:"sipNumberId,omitempty"`
	AgentProfileID *uuid.UUID     `json:"agentProfileId,omitempty"`
	Status         CallbackStatus `json:"status"`
	ResultCallID   *uuid.UUID     `json:"resultCallId,omitempty"`
	Attempts       int            `json:"attempts"`
	CreatedBy      *uuid.UUID     `json:"createdBy,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// IntegrationsRepository persists webhooks, deliveries, SMS and callbacks.
type IntegrationsRepository interface {
	CreateWebhook(ctx context.Context, w *Webhook) error
	UpdateWebhook(ctx context.Context, w *Webhook) error
	DeleteWebhook(ctx context.Context, id uuid.UUID) error
	GetWebhook(ctx context.Context, id uuid.UUID) (*Webhook, error) // with Secret
	ListWebhooks(ctx context.Context, orgID uuid.UUID) ([]Webhook, error)
	ListActiveWebhooksForEvent(ctx context.Context, orgID uuid.UUID, ev EventType) ([]Webhook, error)

	CreateDelivery(ctx context.Context, d *WebhookDelivery) error
	UpdateDelivery(ctx context.Context, d *WebhookDelivery) error
	ListDeliveries(ctx context.Context, webhookID uuid.UUID, limit, offset int) ([]WebhookDelivery, int, error)
	ClaimDueDeliveries(ctx context.Context, n int) ([]WebhookDelivery, error) // pending & due, SKIP LOCKED

	CreateSMS(ctx context.Context, m *SMSMessage) error
	UpdateSMS(ctx context.Context, m *SMSMessage) error
	ListSMS(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]SMSMessage, int, error)

	CreateCallback(ctx context.Context, c *CallbackRequest) error
	UpdateCallback(ctx context.Context, c *CallbackRequest) error
	GetCallback(ctx context.Context, id uuid.UUID) (*CallbackRequest, error)
	ListCallbacks(ctx context.Context, orgID uuid.UUID, status CallbackStatus, limit, offset int) ([]CallbackRequest, int, error)
	ClaimDueCallbacks(ctx context.Context, n int) ([]CallbackRequest, error) // pending & due → dialed, SKIP LOCKED
}

// ===========================================================================
// Inbound routing
// ===========================================================================

// MenuOption maps a DTMF key to a profile.
type MenuOption struct {
	Key            string    `json:"key"` // "1".."9", "0", "*", "#"
	Label          string    `json:"label"`
	AgentProfileID uuid.UUID `json:"agentProfileId"`
}

// RoutingConfig decides who answers an inbound call on a SIP number.
// Resolution order: closed hours → AfterHours; Menu non-empty → play prompt
// and wait for DTMF (fallback DefaultProfile after MenuTimeoutSec); else
// SIPNumber.AgentProfileID.
type RoutingConfig struct {
	BusinessHours     CampaignSchedule `json:"businessHours"` // zero = always open
	AfterHoursProfile *uuid.UUID       `json:"afterHoursProfileId,omitempty"`
	AfterHoursMessage string           `json:"afterHoursMessage"` // spoken then hang up when no profile
	MenuPrompt        string           `json:"menuPrompt"`
	Menu              []MenuOption     `json:"menu"`
	MenuTimeoutSec    int              `json:"menuTimeoutSec"`
	MenuRepeat        int              `json:"menuRepeat"`
}

// IsZero reports whether no routing is configured.
func (r RoutingConfig) IsZero() bool {
	return r.BusinessHours.IsZero() && r.AfterHoursProfile == nil && r.AfterHoursMessage == "" && len(r.Menu) == 0
}

// ResolvedRoute is what the agent bootstrap receives.
type ResolvedRoute struct {
	Mode           string       `json:"mode"` // "direct" | "after_hours" | "menu"
	AgentProfileID *uuid.UUID   `json:"agentProfileId,omitempty"`
	Message        string       `json:"message,omitempty"`
	MenuPrompt     string       `json:"menuPrompt,omitempty"`
	Menu           []MenuOption `json:"menu,omitempty"`
	MenuTimeoutSec int          `json:"menuTimeoutSec,omitempty"`
	MenuRepeat     int          `json:"menuRepeat,omitempty"`
}

// ===========================================================================
// Quota / entitlement port (implemented by internal/billing)
// ===========================================================================

// Entitlements answers "may this org do X right now".
type Entitlements interface {
	// CanStartCall checks subscription status, concurrent-call cap and minutes.
	CanStartCall(ctx context.Context, orgID uuid.UUID) (ok bool, reason string, err error)
	HasFeature(ctx context.Context, orgID uuid.UUID, feature string) (bool, error)
	Limits(ctx context.Context, orgID uuid.UUID) (Plan, error) // effective limits
}
