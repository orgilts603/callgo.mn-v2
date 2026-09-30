import { useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type { Call, CallStats, DailyCallCount, ListResponse } from '@/lib/types'

export const dashboardKeys = {
  stats: ['stats'] as const,
  daily: (days: number) => ['stats', 'daily', days] as const,
  recent: (limit: number) => ['calls', 'recent', limit] as const,
}

export function useStats() {
  return useQuery({ queryKey: dashboardKeys.stats, queryFn: () => api.get<CallStats>('/stats') })
}

export function useDailyStats(days = 14) {
  return useQuery({
    queryKey: dashboardKeys.daily(days),
    queryFn: async () => (await api.get<{ items: DailyCallCount[] }>('/stats/daily', { days })).items ?? [],
  })
}

export function useRecentCalls(limit = 10) {
  return useQuery({
    queryKey: dashboardKeys.recent(limit),
    queryFn: () => api.get<ListResponse<Call>>('/calls', { limit }),
  })
}

/** Refetch dashboard data when call.* events arrive (debounced to absorb bursts). */
export function useLiveDashboardRefresh(delayMs = 800) {
  const qc = useQueryClient()
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null
    const off = useLive.getState().onEvent((ev) => {
      if (!ev.type.startsWith('call.')) return
      if (timer) return
      timer = setTimeout(() => {
        timer = null
        void qc.invalidateQueries({ queryKey: ['stats'] })
        void qc.invalidateQueries({ queryKey: ['calls'] })
      }, delayMs)
    })
    return () => { off(); if (timer) clearTimeout(timer) }
  }, [qc, delayMs])
}
