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
	// DNC is the do-not-call list. When nil the /api/dnc endpoints answer
	// "not configured" and dialing / campaign import skip the check.
	DNC domain.DoNotCallRepository

	// Knowledge is the knowledge-base (RAG) service. When nil the
	// /api/knowledge-* endpoints answer "not configured", the internal
	// knowledge search fails the same way and bootstrap sends knowledge=null.
	Knowledge KnowledgeService
	// KnowledgeRepo lists, reads and deletes knowledge documents. When nil
	// the document endpoints answer "not configured" (GET base returns an
	// empty document list).
	KnowledgeRepo domain.KnowledgeRepository
	// Chunks pages a document's chunks for the preview in
	// GET /api/knowledge-documents/{id}. When nil, KnowledgeRepo is
	// type-asserted to ChunkLister; failing that the preview is empty.
	Chunks ChunkLister

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
	// CampaignStats counts targets for GET /api/campaigns/{id}/stats. When
	// nil, Campaign is type-asserted to CampaignStats; failing that the
	// counts are computed from ListAllTargets.
	CampaignStats CampaignStats
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
	// Start runs the campaign. dryRunLimit > 0 dials only that many targets
	// and then pauses the campaign; 0 is a full run. The HTTP layer passes
	// the start request's dryRunLimit, or the campaign's stored DryRunLimit
	// when the request has none.
	Start(ctx context.Context, campaignID uuid.UUID, dryRunLimit int) error
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

// PreviewResult is the head of an uploaded list for the column-mapping UI.
type PreviewResult struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
	// Mapping maps each header to "phone", "name", "tags" or "var".
	Mapping map[string]string `json:"mapping"`
	// Total is the number of data rows in the file.
	Total int `json:"total"`
	// Format is "csv" or "xlsx".
	Format string `json:"format"`
}

// TargetParser parses a campaign target list (CSV or Excel; columns phone,
// name, extra → vars). filename is the uploaded file name, used with the
// content to detect the format.
type TargetParser interface {
	ParseTargets(r io.Reader, filename string) (ParsedTargets, error)
	// Preview returns the header, up to n rows and the detected mapping.
	Preview(r io.Reader, filename string, n int) (PreviewResult, error)
}

// ContactParser parses a contact list (CSV or Excel).
type ContactParser interface {
	ParseContacts(r io.Reader, filename string) (ParsedContacts, error)
}

// CampaignStats counts a campaign's targets in the database.
type CampaignStats interface {
	CountTargetsByStatus(ctx context.Context, campaignID uuid.UUID) (map[domain.CampaignTargetStatus]int, error)
	CountTargetsByOutcome(ctx context.Context, campaignID uuid.UUID) (map[string]int, error)
}

// DNCInserter is an optional extension of domain.DoNotCallRepository that
// reports whether an entry was created (true) or already existed (false, e
// then holds the existing entry). Used to answer 201 vs 200.
type DNCInserter interface {
	InsertDoNotCall(ctx context.Context, e *domain.DoNotCallEntry) (bool, error)
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

// KnowledgeService manages knowledge bases and runs ingestion and search
// (implemented by internal/knowledge). Errors wrap domain.ErrNotFound,
// domain.ErrInvalid (bad input) and domain.ErrConflict (e.g. changing the
// embedding settings of a base that already has chunks).
type KnowledgeService interface {
	CreateBase(ctx context.Context, kb *domain.KnowledgeBase) error
	UpdateBase(ctx context.Context, kb *domain.KnowledgeBase) error
	DeleteBase(ctx context.Context, id uuid.UUID) error
	GetBase(ctx context.Context, id uuid.UUID) (*domain.KnowledgeBase, error)
	ListBases(ctx context.Context, orgID uuid.UUID) ([]domain.KnowledgeBase, error)
	// AddDocument stores an uploaded file (status processing) and ingests it
	// in the background. r is only valid until AddDocument returns.
	AddDocument(ctx context.Context, kbID uuid.UUID, filename, mime string, r io.Reader) (*domain.KnowledgeDocument, error)
	// AddText stores pasted text as a document and ingests it in the background.
	AddText(ctx context.Context, kbID uuid.UUID, filename, text string) (*domain.KnowledgeDocument, error)
	// Reprocess re-runs ingestion of a document in the background.
	Reprocess(ctx context.Context, docID uuid.UUID) error
	// Search returns the k best hits and the mode used ("hybrid" or "text").
	Search(ctx context.Context, kbID uuid.UUID, query string, k int) (hits []domain.KnowledgeHit, mode string, err error)
	// ContextText returns the base's text for knowledgeMode "context",
	// capped (truncated=true when cut).
	ContextText(ctx context.Context, kbID uuid.UUID) (text string, truncated bool, err error)
}

// ChunkLister pages a document's chunks in seq order and returns the total.
type ChunkLister interface {
	ListChunks(ctx context.Context, docID uuid.UUID, limit, offset int) ([]domain.KnowledgeChunk, int, error)
}

// CampaignDeleter removes a campaign and its targets.
type CampaignDeleter interface {
	DeleteCampaign(ctx context.Context, id uuid.UUID) error
}
