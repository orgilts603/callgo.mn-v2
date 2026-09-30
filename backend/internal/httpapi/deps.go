package httpapi

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// Deps are the collaborators of the HTTP API. Repositories, Telephony and Bus
// are required; the local interfaces below are implemented by other packages
// and adapted by the integrator. Nil optional collaborators make the matching
// endpoints answer with an "internal"/"not configured" error.
type Deps struct {
	Org          domain.OrgRepository
	SIPNumber    domain.SIPNumberRepository
	AgentProfile domain.AgentProfileRepository
	LLMConfig    domain.LLMConfigRepository
	Call         domain.CallRepository
	Contact      domain.ContactRepository
	Campaign     domain.CampaignRepository
	Lexicon      domain.LexiconRepository

	Telephony domain.Telephony
	Bus       domain.EventBus

	Live          LiveHub
	Campaigns     CampaignController
	TargetParser  TargetParser
	ContactParser ContactParser
	LexiconEngine Lexicon
	LLMTester     LLMTester // optional

	// OrgCreator creates organisations for POST /api/auth/register. When nil,
	// Org is type-asserted to OrgCreator (domain.OrgRepository has no create).
	OrgCreator OrgCreator
	// CampaignDeleter removes campaigns for DELETE /api/campaigns/{id}. When
	// nil, Campaign is type-asserted to CampaignDeleter.
	CampaignDeleter CampaignDeleter
	// Ready is an optional readiness probe (e.g. DB ping) for /readyz.
	Ready func(ctx context.Context) error
}

// Config holds HTTP-layer settings.
type Config struct {
	JWTSecret        []byte
	AgentToken       string
	LiveKitAPIKey    string
	LiveKitAPISecret string
	AllowSignup      bool
	CORSOrigins      []string
	// TokenTTL defaults to 24h.
	TokenTTL time.Duration
	// TrustedProxies is the number of reverse proxies in front of the server;
	// when > 0 the client IP is taken from X-Forwarded-For accordingly.
	TrustedProxies int
}

// LiveHub serves the browser WebSocket. initial returns the active calls sent
// in the hello message.
type LiveHub interface {
	ServeWS(w http.ResponseWriter, r *http.Request, orgID uuid.UUID, initial func(ctx context.Context) []domain.Call)
}

// CampaignController drives the outbound dialer engine.
type CampaignController interface {
	Start(ctx context.Context, campaignID uuid.UUID) error
	Pause(ctx context.Context, campaignID uuid.UUID) error
	OnCallEnded(ctx context.Context, call *domain.Call)
}

// RowError is a CSV row that could not be imported.
type RowError struct {
	Row     int    `json:"row"`
	Message string `json:"message"`
}

// ParsedTargets is the result of parsing a campaign CSV.
type ParsedTargets struct {
	Targets []domain.CampaignTarget
	Skipped int
	Errors  []RowError
}

// ParsedContacts is the result of parsing a contacts CSV.
type ParsedContacts struct {
	Contacts []domain.Contact
	Skipped  int
	Errors   []RowError
}

// TargetParser parses a campaign target CSV (columns phone, name, extra → vars).
type TargetParser interface {
	ParseTargets(r io.Reader) (ParsedTargets, error)
}

// ContactParser parses a contacts CSV.
type ContactParser interface {
	ParseContacts(r io.Reader) (ParsedContacts, error)
}

// LexiconHit is one correction applied by the lexicon engine.
type LexiconHit struct {
	ID      uuid.UUID `json:"id"`
	Wrong   string    `json:"wrong"`
	Correct string    `json:"correct"`
}

// Lexicon is the correction engine (cached per org).
type Lexicon interface {
	Apply(ctx context.Context, orgID uuid.UUID, text string) (string, []LexiconHit, error)
	Invalidate(orgID uuid.UUID)
}

// LLMTester performs a one-shot completion against a config (with APIKey).
type LLMTester interface {
	Test(ctx context.Context, cfg domain.LLMConfig, prompt string) (reply string, latency time.Duration, err error)
}

// OrgCreator persists a new organisation (fills ID/CreatedAt when zero).
type OrgCreator interface {
	CreateOrg(ctx context.Context, o *domain.Organization) error
}

// CampaignDeleter removes a campaign and its targets.
type CampaignDeleter interface {
	DeleteCampaign(ctx context.Context, id uuid.UUID) error
}
