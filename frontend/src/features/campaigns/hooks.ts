import { useEffect } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api, HttpError } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type {
  AgentProfile, Call, Campaign, CampaignProgressPayload, CampaignStatus, CampaignTarget, ListResponse, SIPNumber,
} from '@/lib/types'

export interface CampaignDetail { campaign: Campaign; targets: ListResponse<CampaignTarget> }
export interface ImportResult { imported: number; skipped: number; errors: { row: number; message: string }[] }
export interface CreateCampaignResult { campaign: Campaign; targets: ImportResult }
export type CampaignAction = 'start' | 'pause'

export const campaignKeys = {
  all: ['campaigns'] as const,
  detail: (id: string) => ['campaign', id] as const,
  page: (id: string, limit: number, offset: number) => ['campaign', id, { limit, offset }] as const,
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
    mutationFn: ({ id, action }: { id: string; action: CampaignAction }) =>
      api.post<{ campaign: Campaign }>(`/campaigns/${id}/${action}`),
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
    onSuccess: ({ campaign }, { action }) => {
      patchCampaign(qc, campaign)
      toast.success(action === 'start' ? 'Кампанит ажил эхэллээ' : 'Кампанит ажил түр зогслоо')
    },
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
          }, 1000)
        }
      }
    })
    return () => { off(); if (timer) clearTimeout(timer) }
  }, [onEvent, qc, campaignId])
}
