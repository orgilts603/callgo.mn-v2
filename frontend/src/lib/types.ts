// Mirrors backend/internal/domain/domain.go (json tags). FROZEN CONTRACT — do not edit.
export type UUID = string
export type ISODate = string

export type Role = 'owner' | 'admin' | 'operator'
export interface Organization { id: UUID; name: string; slug: string; createdAt: ISODate }
export interface User { id: UUID; orgId: UUID; email: string; name: string; role: Role; createdAt: ISODate }

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
  knowledgeBaseId?: UUID | null; knowledgeMode: KnowledgeMode
  createdAt: ISODate; updatedAt: ISODate
}
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
  allowOutbound: boolean; active: boolean; createdAt: ISODate; updatedAt: ISODate
}

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
export type AgentState = 'initializing' | 'listening' | 'thinking' | 'speaking' | 'idle'

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
