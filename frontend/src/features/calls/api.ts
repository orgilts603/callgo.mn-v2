import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { Call, Campaign, Contact, LexiconCorrection, LexiconScope, TranscriptTurn } from '@/lib/types'

/** Response of `GET /api/calls/{id}`. */
export interface CallDetail { call: Call; turns: TranscriptTurn[]; contact: Contact | null }

/** Body of `PATCH /api/turns/{turnId}`. */
export interface TurnPatchBody { text: string; wrong?: string; correct?: string; scope?: LexiconScope; phonetic?: string }
export interface TurnPatchResponse { turn: TranscriptTurn; correction: LexiconCorrection | null }

export const callKey = (id: string) => ['call', id] as const
export const activeCallsKey = ['calls', 'active'] as const
export const campaignNamesKey = ['calls', 'campaign-names'] as const
export const contactNameKey = (id: string) => ['calls', 'contact-name', id] as const

export const callsApi = {
  get: (id: string) => api.get<CallDetail>(`/calls/${encodeURIComponent(id)}`),
  active: () => api.get<{ items: Call[] }>('/calls/active'),
  hangup: (id: string) => api.post<void>(`/calls/${encodeURIComponent(id)}/hangup`),
  transfer: (id: string, toNumber: string) => api.post<void>(`/calls/${encodeURIComponent(id)}/transfer`, { toNumber }),
  patchTurn: (turnId: string, body: TurnPatchBody) => api.patch<TurnPatchResponse>(`/turns/${encodeURIComponent(turnId)}`, body),
  campaigns: () => api.get<{ items: Campaign[] }>('/campaigns'),
  contact: (id: string) => api.get<{ contact: Contact }>(`/contacts/${encodeURIComponent(id)}`),
}

export function useCallDetail(id: string | null | undefined) {
  return useQuery({
    queryKey: callKey(id ?? ''),
    queryFn: () => callsApi.get(id as string),
    enabled: !!id,
  })
}

const EMPTY_NAMES: ReadonlyMap<string, string> = new Map()
const toNameMap = (r: { items: Campaign[] }): ReadonlyMap<string, string> => new Map(r.items.map((c) => [c.id, c.name] as const))

/** Campaign id → name lookup (one request for all campaigns, cached for a minute). */
export function useCampaignNames(enabled = true): ReadonlyMap<string, string> {
  const q = useQuery({
    queryKey: campaignNamesKey,
    queryFn: callsApi.campaigns,
    enabled,
    staleTime: 60_000,
    select: toNameMap,
  })
  return q.data ?? EMPTY_NAMES
}

/** Stable-ish sort: by seq, then by start time. */
function byOrder(a: TranscriptTurn, b: TranscriptTurn): number {
  return a.seq - b.seq || a.startMs - b.startMs
}

function turnKey(t: TranscriptTurn): string {
  return t.id || `seq:${t.seq}`
}

/** Merge incoming turns into a list: replace by id (or seq when id is empty), keep sorted by seq. */
export function mergeTurns(turns: readonly TranscriptTurn[], incoming: readonly TranscriptTurn[]): TranscriptTurn[] {
  if (incoming.length === 0) return turns as TranscriptTurn[]
  const map = new Map<string, TranscriptTurn>()
  for (const t of turns) map.set(turnKey(t), t)
  for (const t of incoming) map.set(turnKey(t), t)
  return [...map.values()].sort(byOrder)
}

export function isLiveStatus(status: Call['status']): boolean {
  return status === 'active' || status === 'ringing' || status === 'queued'
}
