import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { AgentProfile, DoNotCallEntry, LLMCatalogEntry, LLMConfig, LLMProvider, Organization, SIPNumber, User } from '@/lib/types'

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
  tools: string[]; transferNumber?: string
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
