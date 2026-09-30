// Package domain holds the core entities and port interfaces of CallGo.mn.
// It has no dependencies on infrastructure (hexagonal architecture core).
//
// Ownership: this file is owned by the integrator. Feature agents implement
// the interfaces here and MUST NOT change existing signatures. Adding new
// methods to an interface is allowed only in the package that implements it,
// via a narrower interface local to that package.
package domain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Common errors returned by repositories and services.
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrInvalid      = errors.New("invalid input")
)

// ===========================================================================
// Tenancy & auth
// ===========================================================================

// Organization is a tenant (a company that bought CallGo.mn).
type Organization struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"createdAt"`
}

// Role of a user inside an organisation.
type Role string

const (
	RoleOwner    Role = "owner"
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
)

// User is a human who logs into the CRM.
type User struct {
	ID           uuid.UUID `json:"id"`
	OrgID        uuid.UUID `json:"orgId"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Role         Role      `json:"role"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ===========================================================================
// Telephony configuration (SIP numbers, agent profiles, LLM configs)
// ===========================================================================

// LLMProvider identifies a reasoning-model vendor. The Python agent's LLM
// router maps each provider to a livekit plugin.
type LLMProvider string

const (
	ProviderOpenAI           LLMProvider = "openai"
	ProviderAnthropic        LLMProvider = "anthropic"
	ProviderGoogle           LLMProvider = "google"
	ProviderGroq             LLMProvider = "groq"
	ProviderOllama           LLMProvider = "ollama"
	ProviderOpenAICompatible LLMProvider = "openai_compatible"
)

// LLMConfig is one configured model an organisation can use.
// APIKey is stored encrypted at rest and never returned to the browser
// (only APIKeyHint, e.g. "sk-...q9Zt").
type LLMConfig struct {
	ID          uuid.UUID   `json:"id"`
	OrgID       uuid.UUID   `json:"orgId"`
	Name        string      `json:"name"`
	Provider    LLMProvider `json:"provider"`
	Model       string      `json:"model"`
	BaseURL     string      `json:"baseUrl,omitempty"`
	APIKey      string      `json:"-"`
	APIKeyHint  string      `json:"apiKeyHint,omitempty"`
	Temperature float32     `json:"temperature"`
	MaxTokens   int         `json:"maxTokens"`
	IsDefault   bool        `json:"isDefault"`
	// FallbackID is tried when this model errors or times out.
	FallbackID *uuid.UUID `json:"fallbackId,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// AgentProfile is the persona that answers (or makes) calls: prompt,
// greeting, voice, and which LLM/STT/TTS to use.
type AgentProfile struct {
	ID             uuid.UUID  `json:"id"`
	OrgID          uuid.UUID  `json:"orgId"`
	Name           string     `json:"name"`
	SystemPrompt   string     `json:"systemPrompt"`
	Greeting       string     `json:"greeting"`
	Language       string     `json:"language"` // BCP-47, default "mn"
	LLMConfigID    *uuid.UUID `json:"llmConfigId,omitempty"`
	STTProvider    string     `json:"sttProvider"` // "faster_whisper" | "deepgram" | ...
	STTModel       string     `json:"sttModel"`
	TTSProvider    string     `json:"ttsProvider"` // "piper" | "openai" | ...
	TTSVoice       string     `json:"ttsVoice"`
	MaxDurationSec int        `json:"maxDurationSec"`
	// Tools the LLM may call: "end_call", "transfer_call", "lookup_contact", "schedule_callback".
	Tools          []string  `json:"tools"`
	TransferNumber string    `json:"transferNumber,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// SIPNumber is a DID / extension owned by an organisation. Inbound calls to
// this number are answered by AgentProfileID; outbound campaigns use it as
// caller ID.
type SIPNumber struct {
	ID     uuid.UUID `json:"id"`
	OrgID  uuid.UUID `json:"orgId"`
	Number string    `json:"number"` // E.164, e.g. +97677001234
	Label  string    `json:"label"`
	// LiveKit SIP resources bound to this number.
	InboundTrunkID  string `json:"inboundTrunkId,omitempty"`
	OutboundTrunkID string `json:"outboundTrunkId,omitempty"`
	DispatchRuleID  string `json:"dispatchRuleId,omitempty"`
	// Asterisk side: which PJSIP endpoint/context this number lives on.
	AsteriskEndpoint string     `json:"asteriskEndpoint,omitempty"`
	AgentProfileID   *uuid.UUID `json:"agentProfileId,omitempty"`
	AllowInbound     bool       `json:"allowInbound"`
	AllowOutbound    bool       `json:"allowOutbound"`
	Active           bool       `json:"active"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// ===========================================================================
// CRM entities
// ===========================================================================

// Contact is a person the organisation calls or is called by.
type Contact struct {
	ID        uuid.UUID         `json:"id"`
	OrgID     uuid.UUID         `json:"orgId"`
	Phone     string            `json:"phone"` // E.164
	Name      string            `json:"name"`
	Tags      []string          `json:"tags"`
	Meta      map[string]string `json:"meta"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// CallDirection is the direction of a call from the platform's perspective.
type CallDirection string

const (
	DirectionInbound  CallDirection = "inbound"
	DirectionOutbound CallDirection = "outbound"
)

// CallStatus is the lifecycle state of a call.
type CallStatus string

const (
	StatusQueued    CallStatus = "queued"  // outbound: waiting for a dialer slot
	StatusRinging   CallStatus = "ringing" // INVITE sent / received, not yet answered
	StatusActive    CallStatus = "active"  // media flowing, agent in room
	StatusCompleted CallStatus = "completed"
	StatusFailed    CallStatus = "failed"
	StatusNoAnswer  CallStatus = "no_answer"
	StatusBusy      CallStatus = "busy"
	StatusVoicemail CallStatus = "voicemail"
)

// IsTerminal reports whether the status is final.
func (s CallStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusNoAnswer, StatusBusy, StatusVoicemail:
		return true
	}
	return false
}

// Sentiment is the coarse sentiment of a call as classified by the LLM.
type Sentiment string

const (
	SentimentPositive Sentiment = "positive"
	SentimentNeutral  Sentiment = "neutral"
	SentimentNegative Sentiment = "negative"
)

// Call is a single telephone call and its AI-derived outcome.
type Call struct {
	ID             uuid.UUID     `json:"id"`
	OrgID          uuid.UUID     `json:"orgId"`
	ContactID      *uuid.UUID    `json:"contactId,omitempty"`
	CampaignID     *uuid.UUID    `json:"campaignId,omitempty"`
	SIPNumberID    *uuid.UUID    `json:"sipNumberId,omitempty"`
	AgentProfileID *uuid.UUID    `json:"agentProfileId,omitempty"`
	Direction      CallDirection `json:"direction"`
	Status         CallStatus    `json:"status"`
	FromNumber     string        `json:"fromNumber"`
	ToNumber       string        `json:"toNumber"`
	// LiveKit identifiers.
	RoomName      string         `json:"roomName"`
	SIPCallID     string         `json:"sipCallId,omitempty"`
	ParticipantID string         `json:"participantId,omitempty"`
	StartedAt     time.Time      `json:"startedAt"`
	AnsweredAt    *time.Time     `json:"answeredAt,omitempty"`
	EndedAt       *time.Time     `json:"endedAt,omitempty"`
	DurationSec   int            `json:"durationSec"`
	RecordingURL  string         `json:"recordingUrl,omitempty"`
	Summary       string         `json:"summary,omitempty"`
	Sentiment     Sentiment      `json:"sentiment,omitempty"`
	Intent        string         `json:"intent,omitempty"`
	EndReason     string         `json:"endReason,omitempty"`
	LLMModelUsed  string         `json:"llmModelUsed,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}

// Speaker identifies who produced a transcript turn.
type Speaker string

const (
	SpeakerCustomer Speaker = "customer"
	SpeakerAgent    Speaker = "agent" // the AI agent
	SpeakerHuman    Speaker = "human" // a human operator who took over
)

// TranscriptTurn is one utterance in a call transcript.
type TranscriptTurn struct {
	ID         uuid.UUID `json:"id"`
	CallID     uuid.UUID `json:"callId"`
	Seq        int       `json:"seq"`
	Speaker    Speaker   `json:"speaker"`
	Text       string    `json:"text"`
	RawText    string    `json:"rawText,omitempty"` // before normalisation / corrections
	Confidence float32   `json:"confidence"`
	StartMs    int       `json:"startMs"`
	EndMs      int       `json:"endMs"`
	IsFinal    bool      `json:"isFinal"`
	CreatedAt  time.Time `json:"createdAt"`
}

// CampaignStatus is the lifecycle state of an outbound campaign.
type CampaignStatus string

const (
	CampaignDraft     CampaignStatus = "draft"
	CampaignRunning   CampaignStatus = "running"
	CampaignPaused    CampaignStatus = "paused"
	CampaignCompleted CampaignStatus = "completed"
)

// Campaign is a batch of outbound calls.
type Campaign struct {
	ID             uuid.UUID  `json:"id"`
	OrgID          uuid.UUID  `json:"orgId"`
	Name           string     `json:"name"`
	SIPNumberID    *uuid.UUID `json:"sipNumberId,omitempty"`
	AgentProfileID *uuid.UUID `json:"agentProfileId,omitempty"`
	// Script is extra instruction appended to the profile's system prompt.
	Script      string         `json:"script"`
	Status      CampaignStatus `json:"status"`
	Concurrency int            `json:"concurrency"`
	MaxAttempts int            `json:"maxAttempts"`
	// Schedule limits when the dialer may place calls. Zero value = anytime.
	Schedule CampaignSchedule `json:"schedule"`
	// Outcomes are the structured results the AI must choose from at the end
	// of each call (e.g. agreed / declined / callback / wrong_number). Empty =
	// free-form only.
	Outcomes []CampaignOutcome `json:"outcomes"`
	// DryRunLimit > 0 makes Start dial only that many targets and then pause
	// the campaign automatically so an admin can listen before a full run.
	DryRunLimit int `json:"dryRunLimit"`
	// DryRunDialed counts targets dialed under the current dry-run.
	DryRunDialed int       `json:"dryRunDialed"`
	Total        int       `json:"total"`
	Completed    int       `json:"completed"`
	Failed       int       `json:"failed"`
	Skipped      int       `json:"skipped"` // do-not-call / invalid at import
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// CampaignSchedule is the calling window of a campaign. Times are "HH:MM" in
// Timezone (IANA, default "Asia/Ulaanbaatar"); Weekdays uses time.Weekday
// values (0 = Sunday). Empty Weekdays = every day. PacePerMinute caps how many
// new dials the engine starts per minute (0 = unlimited within Concurrency).
type CampaignSchedule struct {
	Timezone      string         `json:"timezone"`
	Weekdays      []time.Weekday `json:"weekdays"`
	StartTime     string         `json:"startTime"` // "09:00"
	EndTime       string         `json:"endTime"`   // "18:00"
	PacePerMinute int            `json:"pacePerMinute"`
}

// IsZero reports whether no window is configured (dial anytime).
func (s CampaignSchedule) IsZero() bool {
	return s.StartTime == "" && s.EndTime == "" && len(s.Weekdays) == 0 && s.PacePerMinute == 0
}

// CampaignOutcome is one selectable result of a call.
type CampaignOutcome struct {
	Code        string `json:"code"`        // machine key, e.g. "agreed"
	Label       string `json:"label"`       // shown in the UI / Excel, e.g. "Зөвшөөрсөн"
	Description string `json:"description"` // guidance for the LLM on when to pick it
	// Terminal outcomes finish the target; non-terminal ones (e.g. "callback")
	// re-queue it for another attempt if attempts remain.
	Terminal bool `json:"terminal"`
}

// CampaignTargetStatus is per-contact state inside a campaign.
type CampaignTargetStatus string

const (
	TargetPending CampaignTargetStatus = "pending"
	TargetCalling CampaignTargetStatus = "calling"
	TargetDone    CampaignTargetStatus = "done"
	TargetFailed  CampaignTargetStatus = "failed"
	TargetSkipped CampaignTargetStatus = "skipped" // do-not-call list
)

// CampaignTarget is one phone number queued in a campaign.
type CampaignTarget struct {
	ID         uuid.UUID            `json:"id"`
	CampaignID uuid.UUID            `json:"campaignId"`
	ContactID  *uuid.UUID           `json:"contactId,omitempty"`
	Phone      string               `json:"phone"`
	Name       string               `json:"name"`
	Vars       map[string]string    `json:"vars,omitempty"` // CSV extra columns → prompt variables
	Status     CampaignTargetStatus `json:"status"`
	Attempts   int                  `json:"attempts"`
	CallID     *uuid.UUID           `json:"callId,omitempty"`
	// Outcome is the CampaignOutcome.Code chosen by the AI on the last call.
	Outcome     string     `json:"outcome,omitempty"`
	OutcomeNote string     `json:"outcomeNote,omitempty"` // one-line justification from the LLM
	LastError   string     `json:"lastError,omitempty"`
	NextTryAt   *time.Time `json:"nextTryAt,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// DoNotCallEntry is a phone number the organisation must never dial.
type DoNotCallEntry struct {
	ID        uuid.UUID  `json:"id"`
	OrgID     uuid.UUID  `json:"orgId"`
	Phone     string     `json:"phone"` // E.164
	Reason    string     `json:"reason"`
	CreatedBy *uuid.UUID `json:"createdBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// LexiconScope says where a correction applies.
type LexiconScope string

const (
	ScopeSTT  LexiconScope = "stt" // fix recognised text
	ScopeTTS  LexiconScope = "tts" // pronunciation hint for synthesis
	ScopeBoth LexiconScope = "both"
)

// LexiconCorrection is an admin-made fix of a misrecognised word.
// It feeds the normalizer (STT side) and the pronunciation lexicon (TTS side).
type LexiconCorrection struct {
	ID         uuid.UUID    `json:"id"`
	OrgID      uuid.UUID    `json:"orgId"`
	Wrong      string       `json:"wrong"`
	Correct    string       `json:"correct"`
	Phonetic   string       `json:"phonetic,omitempty"`
	Scope      LexiconScope `json:"scope"`
	SourceTurn *uuid.UUID   `json:"sourceTurnId,omitempty"`
	CreatedBy  *uuid.UUID   `json:"createdBy,omitempty"`
	HitCount   int          `json:"hitCount"`
	CreatedAt  time.Time    `json:"createdAt"`
}

// ===========================================================================
// Live events (backend → browser, agent → backend)
// ===========================================================================

// EventType enumerates real-time events. Payload shapes are documented in
// docs/EVENTS.md and mirrored in frontend/src/lib/types.ts.
type EventType string

const (
	EventCallStarted       EventType = "call.started"
	EventCallRinging       EventType = "call.ringing"
	EventCallAnswered      EventType = "call.answered"
	EventCallEnded         EventType = "call.ended"
	EventCallUpdated       EventType = "call.updated" // summary/sentiment/intent filled in
	EventTranscriptPartial EventType = "transcript.partial"
	EventTranscriptFinal   EventType = "transcript.final"
	EventAgentState        EventType = "agent.state" // listening|thinking|speaking
	EventCampaignProgress  EventType = "campaign.progress"
	EventLexiconUpdated    EventType = "lexicon.updated"
	EventSystem            EventType = "system"
)

// Event is a real-time message fanned out to browsers of one organisation.
type Event struct {
	ID      string     `json:"id"`
	Type    EventType  `json:"type"`
	OrgID   uuid.UUID  `json:"orgId"`
	CallID  *uuid.UUID `json:"callId,omitempty"`
	At      time.Time  `json:"at"`
	Payload any        `json:"payload"`
}

// EventBus is the driven port for real-time fan-out. Implemented by
// internal/live (in-process hub); could later be Redis-backed.
type EventBus interface {
	Publish(ctx context.Context, ev Event)
}

// ===========================================================================
// Repository ports
// ===========================================================================

// OrgRepository persists organisations and users.
type OrgRepository interface {
	EnsureDefaultOrg(ctx context.Context) (*Organization, error)
	GetOrg(ctx context.Context, id uuid.UUID) (*Organization, error)
	GetOrgBySlug(ctx context.Context, slug string) (*Organization, error)
	CreateUser(ctx context.Context, u *User) error
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUser(ctx context.Context, id uuid.UUID) (*User, error)
	ListUsers(ctx context.Context, orgID uuid.UUID) ([]User, error)
}

// SIPNumberRepository persists DIDs and their routing.
type SIPNumberRepository interface {
	CreateSIPNumber(ctx context.Context, n *SIPNumber) error
	UpdateSIPNumber(ctx context.Context, n *SIPNumber) error
	DeleteSIPNumber(ctx context.Context, id uuid.UUID) error
	GetSIPNumber(ctx context.Context, id uuid.UUID) (*SIPNumber, error)
	// GetSIPNumberByNumber resolves an inbound DID (any org) — used by the
	// LiveKit webhook / agent dispatch to find the org and agent profile.
	GetSIPNumberByNumber(ctx context.Context, number string) (*SIPNumber, error)
	ListSIPNumbers(ctx context.Context, orgID uuid.UUID) ([]SIPNumber, error)
}

// AgentProfileRepository persists personas.
type AgentProfileRepository interface {
	CreateAgentProfile(ctx context.Context, p *AgentProfile) error
	UpdateAgentProfile(ctx context.Context, p *AgentProfile) error
	DeleteAgentProfile(ctx context.Context, id uuid.UUID) error
	GetAgentProfile(ctx context.Context, id uuid.UUID) (*AgentProfile, error)
	ListAgentProfiles(ctx context.Context, orgID uuid.UUID) ([]AgentProfile, error)
}

// LLMConfigRepository persists model configurations. Implementations must
// encrypt APIKey at rest (AES-GCM with a server-side key) and fill APIKeyHint.
type LLMConfigRepository interface {
	CreateLLMConfig(ctx context.Context, c *LLMConfig) error
	UpdateLLMConfig(ctx context.Context, c *LLMConfig) error
	DeleteLLMConfig(ctx context.Context, id uuid.UUID) error
	// GetLLMConfig returns the config with APIKey decrypted.
	GetLLMConfig(ctx context.Context, id uuid.UUID) (*LLMConfig, error)
	GetDefaultLLMConfig(ctx context.Context, orgID uuid.UUID) (*LLMConfig, error)
	ListLLMConfigs(ctx context.Context, orgID uuid.UUID) ([]LLMConfig, error)
}

// CallFilter narrows a call listing.
type CallFilter struct {
	OrgID      uuid.UUID
	Status     []CallStatus
	Direction  CallDirection
	CampaignID *uuid.UUID
	Search     string // matches phone numbers, contact name, summary
	From       *time.Time
	To         *time.Time
	Limit      int
	Offset     int
}

// CallStats is a dashboard summary.
type CallStats struct {
	TotalCalls     int     `json:"totalCalls"`
	ActiveCalls    int     `json:"activeCalls"`
	CompletedToday int     `json:"completedToday"`
	AvgDurationSec float64 `json:"avgDurationSec"`
	PositiveRatio  float64 `json:"positiveRatio"`
	NegativeRatio  float64 `json:"negativeRatio"`
	InboundToday   int     `json:"inboundToday"`
	OutboundToday  int     `json:"outboundToday"`
}

// DailyCallCount is one bucket of the calls-per-day series.
type DailyCallCount struct {
	Day       time.Time `json:"day"`
	Inbound   int       `json:"inbound"`
	Outbound  int       `json:"outbound"`
	Completed int       `json:"completed"`
	Failed    int       `json:"failed"`
}

// CallRepository persists calls and transcripts.
type CallRepository interface {
	CreateCall(ctx context.Context, c *Call) error
	UpdateCall(ctx context.Context, c *Call) error
	GetCall(ctx context.Context, id uuid.UUID) (*Call, error)
	GetCallByRoom(ctx context.Context, roomName string) (*Call, error)
	ListCalls(ctx context.Context, f CallFilter) ([]Call, int, error)
	ListActiveCalls(ctx context.Context, orgID uuid.UUID) ([]Call, error)
	AddTurn(ctx context.Context, t *TranscriptTurn) error
	UpdateTurnText(ctx context.Context, turnID uuid.UUID, text string) error
	GetTurn(ctx context.Context, turnID uuid.UUID) (*TranscriptTurn, error)
	ListTurns(ctx context.Context, callID uuid.UUID) ([]TranscriptTurn, error)
	Stats(ctx context.Context, orgID uuid.UUID) (CallStats, error)
	DailySeries(ctx context.Context, orgID uuid.UUID, days int) ([]DailyCallCount, error)
}

// ContactRepository persists contacts.
type ContactRepository interface {
	UpsertContact(ctx context.Context, c *Contact) error
	GetContact(ctx context.Context, id uuid.UUID) (*Contact, error)
	GetContactByPhone(ctx context.Context, orgID uuid.UUID, phone string) (*Contact, error)
	ListContacts(ctx context.Context, orgID uuid.UUID, search string, limit, offset int) ([]Contact, int, error)
	DeleteContact(ctx context.Context, id uuid.UUID) error
}

// CampaignRepository persists campaigns and their targets.
type CampaignRepository interface {
	CreateCampaign(ctx context.Context, c *Campaign, targets []CampaignTarget) error
	UpdateCampaign(ctx context.Context, c *Campaign) error
	GetCampaign(ctx context.Context, id uuid.UUID) (*Campaign, error)
	ListCampaigns(ctx context.Context, orgID uuid.UUID) ([]Campaign, error)
	ListRunningCampaigns(ctx context.Context) ([]Campaign, error)
	ListTargets(ctx context.Context, campaignID uuid.UUID, limit, offset int) ([]CampaignTarget, int, error)
	// ClaimTargets atomically moves up to n pending (and due) targets to "calling"
	// using SELECT ... FOR UPDATE SKIP LOCKED so several dialer workers can run.
	ClaimTargets(ctx context.Context, campaignID uuid.UUID, n int) ([]CampaignTarget, error)
	UpdateTarget(ctx context.Context, t *CampaignTarget) error
	CountActiveTargets(ctx context.Context, campaignID uuid.UUID) (int, error)
	DeleteCampaign(ctx context.Context, id uuid.UUID) error
	// ListAllTargets streams every target of a campaign (for exports).
	ListAllTargets(ctx context.Context, campaignID uuid.UUID) ([]CampaignTarget, error)
}

// DoNotCallRepository persists the per-org do-not-call list.
type DoNotCallRepository interface {
	AddDoNotCall(ctx context.Context, e *DoNotCallEntry) error // idempotent on (org, phone)
	RemoveDoNotCall(ctx context.Context, orgID uuid.UUID, phone string) error
	ListDoNotCall(ctx context.Context, orgID uuid.UUID, search string, limit, offset int) ([]DoNotCallEntry, int, error)
	IsDoNotCall(ctx context.Context, orgID uuid.UUID, phone string) (bool, error)
	// FilterDoNotCall returns the subset of phones that are on the list.
	FilterDoNotCall(ctx context.Context, orgID uuid.UUID, phones []string) (map[string]bool, error)
}

// LexiconRepository persists corrections.
type LexiconRepository interface {
	AddCorrection(ctx context.Context, c *LexiconCorrection) error
	UpdateCorrection(ctx context.Context, c *LexiconCorrection) error
	ListCorrections(ctx context.Context, orgID uuid.UUID) ([]LexiconCorrection, error)
	DeleteCorrection(ctx context.Context, id uuid.UUID) error
	IncrementHits(ctx context.Context, ids []uuid.UUID) error
}

// ===========================================================================
// Telephony port (LiveKit / SIP control plane)
// ===========================================================================

// OutboundCallRequest asks the telephony adapter to dial a number.
type OutboundCallRequest struct {
	CallID       uuid.UUID
	RoomName     string
	FromNumber   SIPNumber
	ToNumber     string
	AgentProfile AgentProfile
	// Metadata is passed to the agent worker as job/participant metadata (JSON).
	Metadata map[string]any
	// WaitUntilAnswered blocks the call until the callee picks up (or fails).
	WaitUntilAnswered bool
	RingTimeout       time.Duration
}

// OutboundCallResult is what the adapter learned when dialing.
type OutboundCallResult struct {
	ParticipantID string
	SIPCallID     string
	Answered      bool
	Error         string
}

// Telephony is the driven port for the media/control plane (LiveKit SIP).
// A mock implementation exists for local development without a trunk.
type Telephony interface {
	// EnsureNumberProvisioned creates/updates LiveKit inbound trunk, outbound
	// trunk and dispatch rule for the number and fills the *ID fields.
	EnsureNumberProvisioned(ctx context.Context, n *SIPNumber) error
	DeprovisionNumber(ctx context.Context, n *SIPNumber) error
	// Dial places an outbound call into RoomName and dispatches the agent.
	Dial(ctx context.Context, req OutboundCallRequest) (OutboundCallResult, error)
	// Hangup terminates a call by deleting its room.
	Hangup(ctx context.Context, roomName string) error
	// TransferCall performs a SIP REFER to another number.
	TransferCall(ctx context.Context, roomName, participantID, toNumber string) error
	// ListActiveRooms lists LiveKit rooms (for reconciliation).
	ListActiveRooms(ctx context.Context) ([]string, error)
}
