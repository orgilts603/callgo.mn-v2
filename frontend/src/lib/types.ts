// Mirrors backend/internal/domain/domain.go (json tags). FROZEN CONTRACT — do not edit.
export type UUID = string
export type ISODate = string

export type Role = 'owner' | 'admin' | 'operator'
export type OrgStatus = 'active' | 'suspended' | 'closed'
export interface Organization {
  id: UUID; name: string; slug: string; planCode: string; status: OrgStatus; timezone: string
  settings?: Record<string, unknown>; createdAt: ISODate; updatedAt: ISODate
}
export type UserStatus = 'invited' | 'active' | 'disabled'
export interface User {
  id: UUID; orgId: UUID; email: string; name: string; role: Role; status: UserStatus
  emailVerifiedAt?: ISODate | null; lastLoginAt?: ISODate | null; isPlatformAdmin: boolean; createdAt: ISODate; updatedAt: ISODate
}

export type LLMProvider = 'openai' | 'anthropic' | 'google' | 'groq' | 'ollama' | 'openai_compatible'
export interface LLMConfig {
  id: UUID; orgId: UUID; name: string; provider: LLMProvider; model: string; baseUrl?: string
  apiKeyHint?: string; temperature: number; maxTokens: number; isDefault: boolean; fallbackId?: UUID | null
  createdAt: ISODate; updatedAt: ISODate
}
export interface LLMCatalogEntry { provider: LLMProvider; label: string; models: string[]; needsApiKey: boolean; needsBaseUrl: boolean }

export interface AgentProfile {
  id: UUID; orgId: UUID; name: string; systemPrompt: string; greeting: string; language: string
  llmConfigId?: UUID | null; sttProvider: string; sttModel: string; ttsProvider: string; ttsVoice: string
  maxDurationSec: number; tools: string[]; transferNumber?: string
  knowledgeBaseId?: UUID | null; knowledgeMode: KnowledgeMode; postCallActions: PostCallAction[]
  createdAt: ISODate; updatedAt: ISODate
}
export type PostCallActionType = 'sms' | 'webhook' | 'callback'
export interface PostCallAction { type: PostCallActionType; outcomes: string[]; template: string; webhookId?: UUID | null; delayMin: number }
export type KnowledgeMode = 'off' | 'tool' | 'context'
export interface KnowledgeBase {
  id: UUID; orgId: UUID; name: string; description: string; embeddingLlmConfigId?: UUID | null; embeddingModel: string
  embeddingDims: number; chunkSize: number; chunkOverlap: number; documentCount: number; chunkCount: number
  createdAt: ISODate; updatedAt: ISODate
}
export type DocumentStatus = 'processing' | 'ready' | 'failed'
export interface KnowledgeDocument {
  id: UUID; knowledgeBaseId: UUID; orgId: UUID; filename: string; mimeType: string; sizeBytes: number
  status: DocumentStatus; error?: string; chunkCount: number; charCount: number; createdAt: ISODate; updatedAt: ISODate
}
export interface KnowledgeChunkPreview { id: UUID; seq: number; heading?: string; content: string }
export interface KnowledgeHit { chunkId: UUID; documentId: UUID; filename: string; heading?: string; content: string; score: number }
export interface KnowledgeSearchResponse { hits: KnowledgeHit[]; latencyMs: number; mode: 'hybrid' | 'text' }

export interface SIPNumber {
  id: UUID; orgId: UUID; number: string; label: string; inboundTrunkId?: string; outboundTrunkId?: string
  dispatchRuleId?: string; asteriskEndpoint?: string; agentProfileId?: UUID | null; allowInbound: boolean
  allowOutbound: boolean; active: boolean; routing: RoutingConfig; createdAt: ISODate; updatedAt: ISODate
}
export interface MenuOption { key: string; label: string; agentProfileId: UUID }
export interface RoutingConfig {
  businessHours: CampaignSchedule; afterHoursProfileId?: UUID | null; afterHoursMessage: string
  menuPrompt: string; menu: MenuOption[]; menuTimeoutSec: number; menuRepeat: number
}
export interface ResolvedRoute { mode: 'direct' | 'after_hours' | 'menu'; agentProfileId?: UUID | null; message?: string; menuPrompt?: string; menu?: MenuOption[]; menuTimeoutSec?: number; menuRepeat?: number }

export interface Contact {
  id: UUID; orgId: UUID; phone: string; name: string; tags: string[]; meta: Record<string, string>
  createdAt: ISODate; updatedAt: ISODate
}

export type CallDirection = 'inbound' | 'outbound'
export type CallStatus = 'queued' | 'ringing' | 'active' | 'completed' | 'failed' | 'no_answer' | 'busy' | 'voicemail'
export const TERMINAL_STATUSES: CallStatus[] = ['completed', 'failed', 'no_answer', 'busy', 'voicemail']
export type Sentiment = 'positive' | 'neutral' | 'negative' | ''

export interface Call {
  id: UUID; orgId: UUID; contactId?: UUID | null; campaignId?: UUID | null; sipNumberId?: UUID | null
  agentProfileId?: UUID | null; direction: CallDirection; status: CallStatus; fromNumber: string; toNumber: string
  roomName: string; sipCallId?: string; participantId?: string; startedAt: ISODate; answeredAt?: ISODate | null
  endedAt?: ISODate | null; durationSec: number; recordingUrl?: string; summary?: string; sentiment?: Sentiment
  intent?: string; endReason?: string; outcome?: string; outcomeNote?: string; llmModelUsed?: string; metadata?: Record<string, unknown>
  recording?: RecordingInfo | null; handoff?: HandoffState; operatorId?: UUID | null; usage?: CallUsage | null
  createdAt: ISODate; updatedAt: ISODate
}

export type Speaker = 'customer' | 'agent' | 'human'
export interface TranscriptTurn {
  id: UUID; callId: UUID; seq: number; speaker: Speaker; text: string; rawText?: string; confidence: number
  startMs: number; endMs: number; isFinal: boolean; createdAt: ISODate
}

export type CampaignStatus = 'draft' | 'running' | 'paused' | 'completed'
/** Calling window. Weekdays: 0=Sunday..6=Saturday. Empty = anytime. */
export interface CampaignSchedule {
  timezone: string; weekdays: number[]; startTime: string; endTime: string; pacePerMinute: number
}
export interface CampaignOutcome { code: string; label: string; description: string; terminal: boolean }
export const DEFAULT_OUTCOMES: CampaignOutcome[] = [
  { code: 'agreed', label: 'Зөвшөөрсөн', description: 'Харилцагч саналыг зөвшөөрсөн / үйлдэл хийхээр тохирсон', terminal: true },
  { code: 'declined', label: 'Татгалзсан', description: 'Харилцагч тодорхой татгалзсан', terminal: true },
  { code: 'callback', label: 'Дахин залгах', description: 'Харилцагч дараа залгахыг хүссэн эсвэл одоо ярих боломжгүй', terminal: false },
  { code: 'wrong_number', label: 'Буруу дугаар', description: 'Хариулсан хүн зорилтот хүн биш', terminal: true },
  { code: 'no_contact', label: 'Холбогдоогүй', description: 'Хариулаагүй, завгүй эсвэл дуут шуудан', terminal: true },
]
export interface Campaign {
  id: UUID; orgId: UUID; name: string; sipNumberId?: UUID | null; agentProfileId?: UUID | null; script: string
  status: CampaignStatus; concurrency: number; maxAttempts: number; schedule: CampaignSchedule; outcomes: CampaignOutcome[]
  dryRunLimit: number; dryRunDialed: number; total: number; completed: number; failed: number; skipped: number
  createdAt: ISODate; updatedAt: ISODate
}
export type CampaignTargetStatus = 'pending' | 'calling' | 'done' | 'failed' | 'skipped'
export interface CampaignTarget {
  id: UUID; campaignId: UUID; contactId?: UUID | null; phone: string; name: string; vars?: Record<string, string>
  status: CampaignTargetStatus; attempts: number; callId?: UUID | null; outcome?: string; outcomeNote?: string
  lastError?: string; nextTryAt?: ISODate | null; updatedAt: ISODate
}
export interface CampaignPreview {
  columns: string[]; rows: string[][]; mapping: Record<string, 'phone' | 'name' | 'tags' | 'var'>; total: number; format: 'csv' | 'xlsx'
}
export interface CampaignStats { byStatus: Record<CampaignTargetStatus, number>; byOutcome: { code: string; label: string; count: number }[] }
export interface DoNotCallEntry { id: UUID; orgId: UUID; phone: string; reason: string; createdBy?: UUID | null; createdAt: ISODate }

export type LexiconScope = 'stt' | 'tts' | 'both'
export interface LexiconCorrection {
  id: UUID; orgId: UUID; wrong: string; correct: string; phonetic?: string; scope: LexiconScope
  sourceTurnId?: UUID | null; createdBy?: UUID | null; hitCount: number; createdAt: ISODate
}

export interface CallStats {
  totalCalls: number; activeCalls: number; completedToday: number; avgDurationSec: number
  positiveRatio: number; negativeRatio: number; inboundToday: number; outboundToday: number
}
export interface DailyCallCount { day: ISODate; inbound: number; outbound: number; completed: number; failed: number }

export type EventType =
  | 'call.started' | 'call.ringing' | 'call.answered' | 'call.ended' | 'call.updated'
  | 'transcript.partial' | 'transcript.final' | 'agent.state' | 'campaign.progress' | 'lexicon.updated' | 'system'
  | 'billing.updated' | 'quota.warning' | 'webhook.failed' | 'callback.scheduled'
export type AgentState = 'initializing' | 'listening' | 'thinking' | 'speaking' | 'idle' | 'handoff'

export interface LiveEvent<P = unknown> { id: string; type: EventType; orgId: UUID; callId?: UUID | null; at: ISODate; payload: P }
export interface CallEventPayload { call: Call; endReason?: string; summary?: string; sentiment?: Sentiment; intent?: string; outcome?: string; outcomeNote?: string }
export interface TranscriptPartialPayload { speaker: Speaker; text: string; startMs: number }
export interface TranscriptFinalPayload { turn: TranscriptTurn }
export interface AgentStatePayload { state: AgentState; llmModel?: string }
export interface CampaignProgressPayload { campaign: Campaign; target?: CampaignTarget | null }
export interface LexiconUpdatedPayload { correction: LexiconCorrection; action: 'created' | 'updated' | 'deleted' }
export interface SystemPayload { hello?: boolean; activeCalls?: Call[]; message?: string }

export interface ListResponse<T> { items: T[]; total: number }
export interface ApiError { error: { code: string; message: string } }

// ---- SaaS: identity -------------------------------------------------------
export interface Invitation { id: UUID; orgId: UUID; email: string; role: Role; invitedBy?: UUID | null; expiresAt: ISODate; acceptedAt?: ISODate | null; createdAt: ISODate }
export interface RefreshSession { id: UUID; userId: UUID; orgId: UUID; userAgent: string; ip: string; expiresAt: ISODate; revokedAt?: ISODate | null; lastUsedAt: ISODate; createdAt: ISODate }
export interface APIKey { id: UUID; orgId: UUID; name: string; prefix: string; scopes: string[]; createdBy?: UUID | null; lastUsedAt?: ISODate | null; revokedAt?: ISODate | null; createdAt: ISODate }
export interface AuditEntry { id: UUID; orgId: UUID; actorId?: UUID | null; actorEmail: string; action: string; targetType: string; targetId: string; meta?: Record<string, unknown>; ip: string; at: ISODate }
export interface AuthResponse { token: string; refreshToken: string; user: User; org: Organization; subscription?: Subscription | null }

// ---- SaaS: billing --------------------------------------------------------
export interface Plan {
  code: string; name: string; monthlyMnt: number; includedMinutes: number; overageMntPerMin: number; maxConcurrentCalls: number
  maxAgentProfiles: number; maxUsers: number; maxKnowledgeMb: number; maxSipNumbers: number; features: string[]; trialDays: number; public: boolean
}
export type SubscriptionStatus = 'trialing' | 'active' | 'past_due' | 'canceled'
export interface Subscription {
  id: UUID; orgId: UUID; planCode: string; status: SubscriptionStatus; currentPeriodStart: ISODate; currentPeriodEnd: ISODate
  trialEndsAt?: ISODate | null; canceledAt?: ISODate | null; customLimits?: Plan | null; createdAt: ISODate; updatedAt: ISODate
}
export type UsageKind = 'call_minutes' | 'llm_tokens_in' | 'llm_tokens_out' | 'stt_seconds' | 'tts_chars' | 'sms'
export interface UsageSummary {
  orgId: UUID; periodStart: ISODate; periodEnd: ISODate; calls: number; minutes: number; includedMinutes: number; overageMinutes: number
  overageMnt: number; llmTokens: number; sttSeconds: number; ttsChars: number; sms: number; costMnt: number
}
export interface CallUsage { llmTokensIn: number; llmTokensOut: number; sttSeconds: number; ttsChars: number; llmModel?: string; costMnt: number }
export type InvoiceStatus = 'draft' | 'open' | 'paid' | 'void'
export interface InvoiceLine { description: string; quantity: number; unitMnt: number; amountMnt: number }
export interface Invoice {
  id: UUID; orgId: UUID; number: string; periodStart: ISODate; periodEnd: ISODate; lines: InvoiceLine[]; subtotalMnt: number; vatMnt: number
  totalMnt: number; status: InvoiceStatus; dueAt: ISODate; paidAt?: ISODate | null; createdAt: ISODate
}
export type PaymentStatus = 'pending' | 'paid' | 'failed' | 'expired'
export interface PaymentLink { name: string; logo: string; link: string }
export interface Payment {
  id: UUID; orgId: UUID; invoiceId: UUID; provider: string; providerRef: string; amountMnt: number; status: PaymentStatus
  qrText?: string; qrImage?: string; deepLinks?: PaymentLink[]; expiresAt?: ISODate | null; paidAt?: ISODate | null; createdAt: ISODate
}

// ---- SaaS: recordings & handoff -------------------------------------------
export interface RecordingInfo { egressId?: string; objectKey?: string; sizeBytes: number; durationSec: number; status: 'recording' | 'ready' | 'failed' | 'deleted'; startedAt?: ISODate | null; endedAt?: ISODate | null }
export type HandoffState = '' | 'requested' | 'active' | 'ended'
export interface HandoffToken { token: string; url: string; roomName: string; identity: string }

// ---- SaaS: integrations ---------------------------------------------------
export interface Webhook {
  id: UUID; orgId: UUID; url: string; secretHint: string; events: string[]; active: boolean; description: string
  failureCount: number; lastStatus: number; lastAt?: ISODate | null; createdAt: ISODate; updatedAt: ISODate
}
export interface WebhookDelivery {
  id: UUID; webhookId: UUID; eventId: string; eventType: EventType; status: 'pending' | 'delivered' | 'failed'; attempts: number
  responseCode: number; lastError?: string; nextTryAt?: ISODate | null; createdAt: ISODate; updatedAt: ISODate
}
export interface SMSMessage { id: UUID; orgId: UUID; callId?: UUID | null; to: string; body: string; provider: string; providerRef?: string; status: 'queued' | 'sent' | 'failed'; error?: string; createdAt: ISODate; sentAt?: ISODate | null }
export interface SMSConfig { provider: 'mock' | 'http'; from: string; configured: boolean; url?: string; bodyTemplate?: string }
export type CallbackStatus = 'pending' | 'dialed' | 'done' | 'canceled' | 'failed'
export interface CallbackRequest {
  id: UUID; orgId: UUID; sourceCallId?: UUID | null; contactId?: UUID | null; phone: string; name: string; note: string; dueAt: ISODate
  sipNumberId?: UUID | null; agentProfileId?: UUID | null; status: CallbackStatus; resultCallId?: UUID | null; attempts: number
  createdBy?: UUID | null; createdAt: ISODate; updatedAt: ISODate
}

// ---- SaaS: analytics ------------------------------------------------------
export interface AnalyticsOverview {
  calls: number; answered: number; answerRate: number; avgDurationSec: number; totalMinutes: number; costMnt: number; costPerCallMnt: number
  sentiment: { positive: number; neutral: number; negative: number }; outcomes: { code: string; label: string; count: number }[]
  byDirection: { inbound: number; outbound: number }
}
export interface AnalyticsPoint { ts: ISODate; calls: number; answered: number; minutes: number; costMnt: number }
export interface HeatmapCell { weekday: number; hour: number; calls: number; answerRate: number }
export interface ProfileAnalytics { profileId: UUID; name: string; calls: number; answerRate: number; avgDurationSec: number; positiveRate: number; costMnt: number; outcomes: Record<string, number> }
export interface CampaignAnalytics { campaignId: UUID; name: string; total: number; done: number; failed: number; skipped: number; outcomes: Record<string, number>; minutes: number; costMnt: number }

// ---- SaaS: platform admin -------------------------------------------------
export interface AdminOrgRow { org: Organization; subscription?: Subscription | null; usage?: UsageSummary | null; users: number }
export interface AdminStats { orgs: number; activeSubscriptions: number; mrrMnt: number; callsToday: number; minutesToday: number }
