import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type {
  AgentProfile, DoNotCallEntry, PostCallAction, ResolvedRoute, RoutingConfig, SMSConfig, SMSMessage, Webhook, WebhookDelivery, KnowledgeBase, KnowledgeChunkPreview, KnowledgeDocument, KnowledgeMode, KnowledgeSearchResponse,
  LLMCatalogEntry, LLMConfig, LLMProvider, Organization, SIPNumber, User,
} from '@/lib/types'
import type { APIKey, AuditEntry, Invitation, Organization as IdentityOrg, Plan, Role, Subscription, User as IdentityUser, UserStatus } from '@/lib/types'
import { useAuth } from '@/app/auth'

const KEYS = {
  sip: ['settings', 'sip-numbers'] as const,
  profiles: ['settings', 'agent-profiles'] as const,
  llm: ['settings', 'llm-configs'] as const,
  catalog: ['settings', 'llm-catalog'] as const,
  me: ['settings', 'me'] as const,
}

// ---- SIP numbers ----
export interface SIPNumberBody {
  number: string; label: string; agentProfileId: string | null; allowInbound: boolean; allowOutbound: boolean
  asteriskEndpoint?: string; active?: boolean
}

export function useSIPNumbers() {
  return useQuery({ queryKey: KEYS.sip, queryFn: async () => (await api.get<{ items: SIPNumber[] }>('/sip-numbers')).items ?? [] })
}
export function useSaveSIPNumber() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id?: string; body: SIPNumberBody }) =>
      id ? api.put<{ sipNumber: SIPNumber }>(`/sip-numbers/${id}`, body) : api.post<{ sipNumber: SIPNumber }>('/sip-numbers', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.sip }),
  })
}
export function useDeleteSIPNumber() {
  const qc = useQueryClient()
  return useMutation({ mutationFn: (id: string) => api.delete(`/sip-numbers/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.sip }) })
}
export function useProvisionSIPNumber() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.post<{ sipNumber: SIPNumber }>(`/sip-numbers/${id}/provision`),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.sip }),
  })
}

// ---- Agent profiles ----
export interface AgentProfileBody {
  name: string; systemPrompt: string; greeting: string; language: string; llmConfigId: string | null
  sttProvider: string; sttModel: string; ttsProvider: string; ttsVoice: string; maxDurationSec: number
  tools: string[]; transferNumber?: string; knowledgeBaseId: string | null; knowledgeMode: KnowledgeMode
  postCallActions?: PostCallAction[]
}

export function useAgentProfiles() {
  return useQuery({ queryKey: KEYS.profiles, queryFn: async () => (await api.get<{ items: AgentProfile[] }>('/agent-profiles')).items ?? [] })
}
export function useSaveAgentProfile() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id?: string; body: AgentProfileBody }) =>
      id ? api.put<{ profile: AgentProfile }>(`/agent-profiles/${id}`, body) : api.post<{ profile: AgentProfile }>('/agent-profiles', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.profiles }),
  })
}
export function useDeleteAgentProfile() {
  const qc = useQueryClient()
  return useMutation({ mutationFn: (id: string) => api.delete(`/agent-profiles/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.profiles }) })
}

// ---- LLM configs ----
export interface LLMConfigBody {
  name: string; provider: LLMProvider; model: string; baseUrl?: string; apiKey: string
  temperature: number; maxTokens: number; isDefault: boolean; fallbackId: string | null
}
export interface LLMTestResult { ok: boolean; reply: string; latencyMs: number; error?: string }

export function useLLMConfigs() {
  return useQuery({ queryKey: KEYS.llm, queryFn: async () => (await api.get<{ items: LLMConfig[] }>('/llm-configs')).items ?? [] })
}
export function useLLMCatalog() {
  return useQuery({
    queryKey: KEYS.catalog, staleTime: 5 * 60_000,
    queryFn: async () => (await api.get<{ providers: LLMCatalogEntry[] }>('/llm-configs/catalog')).providers ?? [],
  })
}
export function useSaveLLMConfig() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id?: string; body: LLMConfigBody }) =>
      id ? api.put<{ config: LLMConfig }>(`/llm-configs/${id}`, body) : api.post<{ config: LLMConfig }>('/llm-configs', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.llm }),
  })
}
export function useDeleteLLMConfig() {
  const qc = useQueryClient()
  return useMutation({ mutationFn: (id: string) => api.delete(`/llm-configs/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.llm }) })
}
export function useTestLLMConfig() {
  return useMutation({ mutationFn: ({ id, prompt }: { id: string; prompt?: string }) => api.post<LLMTestResult>(`/llm-configs/${id}/test`, { prompt }) })
}

// ---- Me ----
export function useMe() {
  return useQuery({ queryKey: KEYS.me, queryFn: () => api.get<{ user: User; org: Organization }>('/auth/me') })
}

// ---- Do-not-call list ----
export const dncKey = ['settings', 'dnc'] as const
export interface DNCImportResult { imported: number; skipped: number; errors: { row: number; message: string }[] | null }

export function useDNC(params: { q: string; limit: number; offset: number }) {
  return useQuery({
    queryKey: [...dncKey, params] as const,
    queryFn: () => api.get<{ items: DoNotCallEntry[]; total: number }>('/dnc', params),
    placeholderData: (prev) => prev,
  })
}
export function useAddDNC() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { phone: string; reason?: string }) => api.post<{ entry: DoNotCallEntry }>('/dnc', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: dncKey }),
  })
}
export function useDeleteDNC() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (phone: string) => api.delete(`/dnc/${encodeURIComponent(phone)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: dncKey }),
  })
}
export function useImportDNC() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (file: File) => {
      const fd = new FormData()
      fd.append('file', file)
      return api.post<DNCImportResult>('/dnc/import', fd)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: dncKey }),
  })
}

// ---- Knowledge bases (RAG) ----
export const knowledgeKey = ['settings', 'knowledge'] as const
export const KNOWLEDGE_POLL_MS = 3000
export interface KnowledgeBaseBody {
  name: string; description?: string; embeddingLlmConfigId?: string | null; embeddingModel?: string
  chunkSize?: number; chunkOverlap?: number
}
export interface KnowledgeBaseDetailData { knowledgeBase: KnowledgeBase; documents: KnowledgeDocument[] }
export interface DocumentChunksData { document: KnowledgeDocument; chunks: KnowledgeChunkPreview[] }
export const CHUNK_PAGE_SIZE = 50

export function useKnowledgeBases() {
  return useQuery({
    queryKey: [...knowledgeKey, 'bases'] as const,
    queryFn: async () => (await api.get<{ items: KnowledgeBase[] }>('/knowledge-bases')).items ?? [],
  })
}
export function useKnowledgeBase(id: string | undefined) {
  return useQuery({
    queryKey: [...knowledgeKey, 'base', id] as const, enabled: !!id,
    queryFn: () => api.get<KnowledgeBaseDetailData>(`/knowledge-bases/${id}`),
  })
}
export function useCreateKnowledgeBase() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: KnowledgeBaseBody) => api.post<{ knowledgeBase: KnowledgeBase }>('/knowledge-bases', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: knowledgeKey }),
  })
}
export function useUpdateKnowledgeBase() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: KnowledgeBaseBody }) => api.put<{ knowledgeBase: KnowledgeBase }>(`/knowledge-bases/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: knowledgeKey }),
  })
}
export function useDeleteKnowledgeBase() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.delete(`/knowledge-bases/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: knowledgeKey })
      qc.invalidateQueries({ queryKey: KEYS.profiles })
    },
  })
}

/**
 * Documents of a base. `polling` forces the 3 s refetch on/off; when omitted the list polls
 * automatically for as long as any document is still `processing`.
 */
export function useKnowledgeDocuments(kbId: string | undefined, opts: { polling?: boolean } = {}) {
  return useQuery({
    queryKey: [...knowledgeKey, 'documents', kbId] as const, enabled: !!kbId,
    queryFn: async () => (await api.get<{ items: KnowledgeDocument[] }>(`/knowledge-bases/${kbId}/documents`)).items ?? [],
    refetchInterval: (q) => {
      const auto = q.state.data?.some((d) => d.status === 'processing') ?? false
      return (opts.polling ?? auto) ? KNOWLEDGE_POLL_MS : false
    },
  })
}
export function useUploadDocument(kbId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (file: File) => {
      const fd = new FormData()
      fd.append('file', file)
      return api.post<{ document: KnowledgeDocument }>(`/knowledge-bases/${kbId}/documents`, fd)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: knowledgeKey }),
  })
}
export function useAddText(kbId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { filename: string; text: string }) => api.post<{ document: KnowledgeDocument }>(`/knowledge-bases/${kbId}/documents`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: knowledgeKey }),
  })
}
export function useDeleteDocument() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (docId: string) => api.delete(`/knowledge-documents/${docId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: knowledgeKey }),
  })
}
export function useReprocessDocument() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (docId: string) => api.post(`/knowledge-documents/${docId}/reprocess`),
    onSuccess: () => qc.invalidateQueries({ queryKey: knowledgeKey }),
  })
}
export function useKnowledgeSearch(kbId: string) {
  return useMutation({
    mutationFn: (body: { query: string; k: number }) => api.post<KnowledgeSearchResponse>(`/knowledge-bases/${kbId}/search`, body),
  })
}
export function useDocumentChunks(docId: string | null, offset = 0) {
  return useQuery({
    queryKey: [...knowledgeKey, 'chunks', docId, offset] as const, enabled: !!docId,
    queryFn: () => api.get<DocumentChunksData>(`/knowledge-documents/${docId}`, { offset }),
    placeholderData: (prev) => prev,
  })
}

// ---- Feature gate (403 feature_unavailable) ----
/** Duck-typed so it also works when `@/lib/api` is mocked (no `HttpError` export). */
export function isFeatureUnavailable(e: unknown): boolean {
  if (typeof e !== 'object' || e === null) return false
  const err = e as { status?: number; code?: string }
  return err.status === 403 && err.code === 'feature_unavailable'
}

// ---- Inbound routing ----
export function useSaveRouting() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, routing }: { id: string; routing: RoutingConfig }) => api.put<{ sipNumber?: SIPNumber }>(`/sip-numbers/${id}/routing`, routing),
    onSuccess: () => qc.invalidateQueries({ queryKey: KEYS.sip }),
  })
}
export function useResolveRoute() {
  return useMutation({
    mutationFn: async ({ id, at }: { id: string; at: string }) =>
      (await api.post<{ route: ResolvedRoute }>(`/sip-numbers/${id}/routing/resolve`, undefined, { at })).route,
  })
}

// ---- Webhooks ----
export const webhooksKey = ['settings', 'webhooks'] as const
export interface WebhookCreateBody { url: string; events: string[]; description?: string }
export interface WebhookUpdateBody { url?: string; events?: string[]; active?: boolean; description?: string }
export const DELIVERY_PAGE_SIZE = 20

export function useWebhooks() {
  return useQuery({
    queryKey: webhooksKey, retry: false,
    queryFn: async () => (await api.get<{ items: Webhook[] }>('/webhooks')).items ?? [],
  })
}
export function useCreateWebhook() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: WebhookCreateBody) => api.post<{ webhook: Webhook; secret: string }>('/webhooks', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: webhooksKey }),
  })
}
export function useUpdateWebhook() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: WebhookUpdateBody }) => api.put<{ webhook: Webhook }>(`/webhooks/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: webhooksKey }),
  })
}
export function useDeleteWebhook() {
  const qc = useQueryClient()
  return useMutation({ mutationFn: (id: string) => api.delete(`/webhooks/${id}`), onSuccess: () => qc.invalidateQueries({ queryKey: webhooksKey }) })
}
export function useRotateWebhookSecret() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.post<{ secret: string }>(`/webhooks/${id}/rotate-secret`),
    onSuccess: () => qc.invalidateQueries({ queryKey: webhooksKey }),
  })
}
export function useTestWebhook() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.post<{ delivery: WebhookDelivery }>(`/webhooks/${id}/test`),
    onSuccess: () => qc.invalidateQueries({ queryKey: webhooksKey }),
  })
}
export function useWebhookDeliveries(id: string | null, offset: number) {
  return useQuery({
    queryKey: [...webhooksKey, 'deliveries', id, offset] as const, enabled: !!id,
    queryFn: () => api.get<{ items: WebhookDelivery[]; total: number }>(`/webhooks/${id}/deliveries`, { limit: DELIVERY_PAGE_SIZE, offset }),
    placeholderData: (prev) => prev,
  })
}
export function useRetryDelivery() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.post<{ delivery: WebhookDelivery }>(`/webhook-deliveries/${id}/retry`),
    onSuccess: () => qc.invalidateQueries({ queryKey: webhooksKey }),
  })
}

// ---- SMS ----
export const smsKey = ['settings', 'sms'] as const
export const SMS_PAGE_SIZE = 20
export interface SMSConfigBody { provider: 'mock' | 'http'; url?: string; apiKey?: string; from?: string; bodyTemplate?: string }

export function useSMSConfig() {
  return useQuery({ queryKey: [...smsKey, 'config'] as const, retry: false, queryFn: () => api.get<SMSConfig>('/sms/config') })
}
export function useSaveSMSConfig() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: SMSConfigBody) => api.put<SMSConfig>('/sms/config', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: smsKey }),
  })
}
export function useSendSMS() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { to: string; body: string; callId?: string }) => api.post<{ message: SMSMessage }>('/sms/send', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: [...smsKey, 'messages'] }),
  })
}
export function useSMSMessages(offset: number, enabled = true) {
  return useQuery({
    queryKey: [...smsKey, 'messages', offset] as const, enabled, retry: false,
    queryFn: () => api.get<{ items: SMSMessage[]; total: number }>('/sms', { limit: SMS_PAGE_SIZE, offset }),
    placeholderData: (prev) => prev,
  })
}

// ---- Identity: organization, members, invitations, API keys, audit (F1) ----
export const identityKey = ['settings', 'identity'] as const
export const orgKey = [...identityKey, 'org'] as const
export const membersKey = [...identityKey, 'members'] as const
export const apiKeysKey = [...identityKey, 'api-keys'] as const
export const auditKey = [...identityKey, 'audit'] as const

export interface OrgData { org: IdentityOrg; subscription?: Subscription | null; plan?: Plan | null }
export interface OrgUpdateBody { name?: string; timezone?: string; settings?: Record<string, unknown> }
export interface MembersData { items: IdentityUser[]; invitations: Invitation[] }
export interface MemberUpdateBody { role?: Role; status?: Extract<UserStatus, 'active' | 'disabled'> }
export interface CreateAPIKeyResult { apiKey: APIKey; plaintext: string }
export interface AuditFilters { actorId?: string; action?: string; from?: string; to?: string; limit: number; offset: number }

export function useOrg() {
  return useQuery({ queryKey: orgKey, queryFn: () => api.get<OrgData>('/org') })
}
export function useUpdateOrg() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: OrgUpdateBody) => api.put<{ org: IdentityOrg }>('/org', body),
    onSuccess: (res) => {
      if (res?.org) useAuth.getState().setOrg(res.org)
      qc.invalidateQueries({ queryKey: orgKey })
      qc.invalidateQueries({ queryKey: KEYS.me })
    },
  })
}

export function useMembers(enabled = true) {
  return useQuery({
    queryKey: membersKey, enabled, retry: false,
    queryFn: async () => {
      const res = await api.get<Partial<MembersData>>('/org/members')
      return { items: res?.items ?? [], invitations: res?.invitations ?? [] } satisfies MembersData
    },
  })
}
export function useInviteMember() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { email: string; role: Role }) => api.post<{ invitation: Invitation }>('/org/invitations', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: membersKey }),
  })
}
/**
 * The contract has no dedicated resend endpoint: re-issuing POST /org/invitations with the same
 * email + role creates a fresh token (new expiry) and sends the email again.
 */
export function useResendInvitation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (inv: Pick<Invitation, 'email' | 'role'>) => api.post<{ invitation: Invitation }>('/org/invitations', { email: inv.email, role: inv.role }),
    onSuccess: () => qc.invalidateQueries({ queryKey: membersKey }),
  })
}
export function useCancelInvitation() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.delete(`/org/invitations/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: membersKey }),
  })
}
export function useUpdateMember() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: MemberUpdateBody }) => api.put<{ user: IdentityUser }>(`/org/members/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: membersKey }),
  })
}
export function useRemoveMember() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.delete(`/org/members/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: membersKey }),
  })
}

export function useAPIKeys() {
  return useQuery({
    queryKey: apiKeysKey, retry: false,
    queryFn: async () => (await api.get<{ items: APIKey[] }>('/org/api-keys'))?.items ?? [],
  })
}
export function useCreateAPIKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: { name: string; scopes: string[] }) => api.post<CreateAPIKeyResult>('/org/api-keys', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: apiKeysKey }),
  })
}
export function useRevokeAPIKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.delete(`/org/api-keys/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: apiKeysKey }),
  })
}

export function useAuditLog(filters: AuditFilters) {
  return useQuery({
    queryKey: [...auditKey, filters] as const, retry: false,
    queryFn: async () => {
      const res = await api.get<{ items: AuditEntry[]; total: number }>('/org/audit', { ...filters })
      return { items: res?.items ?? [], total: res?.total ?? 0 }
    },
    placeholderData: (prev) => prev,
  })
}
