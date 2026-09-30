import { useEffect } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, getToken, HttpError } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type {
  AgentProfile, Call, Campaign, CampaignOutcome, CampaignPreview, CampaignProgressPayload, CampaignSchedule, CampaignStats,
  CampaignStatus, CampaignTarget, ListResponse, SIPNumber,
} from '@/lib/types'

export interface CampaignDetail { campaign: Campaign; targets: ListResponse<CampaignTarget> }
export interface ImportResult { imported: number; skipped: number; errors: { row: number; message: string }[] }
export interface CreateCampaignResult { campaign: Campaign; targets: ImportResult }
export type CampaignAction = 'start' | 'pause'
export interface CampaignUpdateBody {
  name?: string; script?: string; sipNumberId?: string; agentProfileId?: string; concurrency?: number; maxAttempts?: number
  schedule?: Partial<CampaignSchedule>; outcomes?: CampaignOutcome[]; dryRunLimit?: number
}

export const campaignKeys = {
  all: ['campaigns'] as const,
  detail: (id: string) => ['campaign', id] as const,
  page: (id: string, limit: number, offset: number) => ['campaign', id, { limit, offset }] as const,
  // Deliberately NOT under ['campaign', id]: detail-cache patchers assume that prefix holds CampaignDetail.
  stats: (id: string) => ['campaign-stats', id] as const,
  preview: ['campaigns', 'preview'] as const,
}

export const PAGE_SIZE = 50

export function useCampaigns() {
  return useQuery({
    queryKey: campaignKeys.all,
    queryFn: async () => (await api.get<{ items: Campaign[] }>('/campaigns')).items ?? [],
  })
}

export function useCampaign(id: string | undefined, opts: { limit?: number; offset?: number } = {}) {
  const limit = opts.limit ?? PAGE_SIZE
  const offset = opts.offset ?? 0
  return useQuery({
    queryKey: campaignKeys.page(id ?? '', limit, offset),
    queryFn: () => api.get<CampaignDetail>(`/campaigns/${id}`, { limit, offset }),
    enabled: !!id,
    placeholderData: keepPreviousData,
    refetchInterval: (q) => (q.state.data?.campaign.status === 'running' ? 10_000 : false),
  })
}

export function useSipNumbers() {
  return useQuery({
    queryKey: ['sip-numbers'],
    queryFn: async () => (await api.get<{ items: SIPNumber[] }>('/sip-numbers')).items ?? [],
  })
}

export function useAgentProfiles() {
  return useQuery({
    queryKey: ['agent-profiles'],
    queryFn: async () => (await api.get<{ items: AgentProfile[] }>('/agent-profiles')).items ?? [],
  })
}

const errMsg = (e: unknown) => (e instanceof HttpError || e instanceof Error ? e.message : 'Алдаа гарлаа')

const nextStatus: Record<CampaignAction, CampaignStatus> = { start: 'running', pause: 'paused' }

/** Start / pause with optimistic status update in list and detail caches. */
export function useCampaignAction() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, action, dryRunLimit }: { id: string; action: CampaignAction; dryRunLimit?: number }) =>
      dryRunLimit === undefined
        ? api.post<{ campaign: Campaign }>(`/campaigns/${id}/${action}`)
        : api.post<{ campaign: Campaign }>(`/campaigns/${id}/${action}`, { dryRunLimit }),
    onMutate: async ({ id, action }) => {
      await qc.cancelQueries({ queryKey: campaignKeys.all })
      await qc.cancelQueries({ queryKey: campaignKeys.detail(id) })
      const prevList = qc.getQueryData<Campaign[]>(campaignKeys.all)
      const prevDetail = qc.getQueriesData<CampaignDetail>({ queryKey: campaignKeys.detail(id) })
      const status = nextStatus[action]
      qc.setQueryData<Campaign[]>(campaignKeys.all, (old) => old?.map((c) => (c.id === id ? { ...c, status } : c)))
      qc.setQueriesData<CampaignDetail>({ queryKey: campaignKeys.detail(id) }, (old) =>
        old ? { ...old, campaign: { ...old.campaign, status } } : old)
      return { prevList, prevDetail }
    },
    onError: (e, _v, ctx) => {
      if (ctx?.prevList) qc.setQueryData(campaignKeys.all, ctx.prevList)
      ctx?.prevDetail.forEach(([key, data]) => qc.setQueryData(key, data))
      toast.error(errMsg(e))
    },
    onSuccess: ({ campaign }, { action, dryRunLimit }) => {
      patchCampaign(qc, campaign)
      void qc.invalidateQueries({ queryKey: campaignKeys.stats(campaign.id) })
      toast.success(action === 'pause' ? 'Кампанит ажил түр зогслоо'
        : dryRunLimit ? `Туршилт эхэллээ: эхний ${dryRunLimit} дугаар` : 'Кампанит ажил эхэллээ')
    },
  })
}

/** PUT /api/campaigns/{id}. */
export function useUpdateCampaign() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: CampaignUpdateBody }) => api.put<{ campaign: Campaign }>(`/campaigns/${id}`, body),
    onSuccess: ({ campaign }) => {
      patchCampaign(qc, campaign)
      toast.success('Тохиргоо хадгалагдлаа')
    },
    onError: (e) => toast.error(errMsg(e)),
  })
}

/** GET /api/campaigns/{id}/stats (byStatus incl. skipped, byOutcome). Re-keyed by progress counters so live updates refetch. */
export function useCampaignStats(campaign: Campaign | undefined) {
  const id = campaign?.id
  const version = campaign ? `${campaign.completed}/${campaign.failed}/${campaign.skipped ?? 0}/${campaign.status}` : ''
  return useQuery({
    queryKey: [...campaignKeys.stats(id ?? ''), version],
    queryFn: () => api.get<CampaignStats>(`/campaigns/${id}/stats`),
    enabled: !!id,
    placeholderData: keepPreviousData,
    refetchInterval: campaign?.status === 'running' ? 10_000 : false,
  })
}

/** POST /api/campaigns/preview (multipart) — backend parses CSV and Excel. */
export function useCampaignPreview() {
  return useMutation({
    mutationKey: campaignKeys.preview,
    mutationFn: (file: File) => {
      const fd = new FormData()
      fd.append('file', file)
      return api.post<CampaignPreview>('/campaigns/preview', fd)
    },
  })
}

function filenameFrom(res: Response, fallback: string): string {
  const cd = res.headers?.get?.('Content-Disposition') ?? ''
  const m = /filename\*?=(?:UTF-8'')?"?([^";]+)"?/i.exec(cd)
  if (m) { try { return decodeURIComponent(m[1]) } catch { return m[1] } }
  return fallback
}

/** Downloads GET /api/campaigns/{id}/export.xlsx with the bearer token (a plain link cannot send it). */
export async function downloadCampaignExport(id: string, name: string): Promise<void> {
  const token = getToken()
  const res = await fetch(`/api/campaigns/${id}/export.xlsx`, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
  if (!res.ok) throw new Error(res.status === 404 ? 'Кампанит ажил олдсонгүй' : 'Excel татаж чадсангүй')
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filenameFrom(res, `${name.trim() || 'campaign'}.xlsx`)
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export function useExportCampaign() {
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) => downloadCampaignExport(id, name),
    onError: (e) => toast.error(errMsg(e)),
  })
}

export function useDeleteCampaign() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.delete(`/campaigns/${id}`),
    onSuccess: (_d, id) => {
      qc.setQueryData<Campaign[]>(campaignKeys.all, (old) => old?.filter((c) => c.id !== id))
      qc.removeQueries({ queryKey: campaignKeys.detail(id) })
      toast.success('Кампанит ажил устгагдлаа')
    },
    onError: (e) => toast.error(errMsg(e)),
  })
}

type QC = ReturnType<typeof useQueryClient>

export function patchCampaign(qc: QC, campaign: Campaign, target?: CampaignTarget | null) {
  qc.setQueryData<Campaign[]>(campaignKeys.all, (old) =>
    old ? (old.some((c) => c.id === campaign.id) ? old.map((c) => (c.id === campaign.id ? campaign : c)) : [campaign, ...old]) : old)
  qc.setQueriesData<CampaignDetail>({ queryKey: campaignKeys.detail(campaign.id) }, (old) => {
    if (!old) return old
    const items = target ? old.targets.items.map((t) => (t.id === target.id ? { ...t, ...target } : t)) : old.targets.items
    return { campaign, targets: { ...old.targets, items } }
  })
}

/** Subscribes to live events and patches react-query caches. */
export function useCampaignLive(campaignId?: string) {
  const qc = useQueryClient()
  const onEvent = useLive((s) => s.onEvent)
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null
    const off = onEvent((ev) => {
      if (ev.type === 'campaign.progress') {
        const p = ev.payload as CampaignProgressPayload
        if (p?.campaign) patchCampaign(qc, p.campaign, p.target)
      } else if (campaignId && ev.type.startsWith('call.')) {
        const call = (ev.payload as { call?: Call }).call
        if (call?.campaignId === campaignId && !timer) {
          timer = setTimeout(() => {
            timer = null
            void qc.invalidateQueries({ queryKey: campaignKeys.detail(campaignId) })
            void qc.invalidateQueries({ queryKey: campaignKeys.stats(campaignId) })
          }, 1000)
        }
      }
    })
    return () => { off(); if (timer) clearTimeout(timer) }
  }, [onEvent, qc, campaignId])
}
