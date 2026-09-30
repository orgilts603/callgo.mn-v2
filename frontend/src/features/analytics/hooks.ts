import { useQuery } from '@tanstack/react-query'
import { api, getToken, HttpError, refreshAccessToken } from '@/lib/api'
import type { AnalyticsOverview, AnalyticsPoint, CampaignAnalytics, HeatmapCell, ProfileAnalytics } from '@/lib/types'

/** Calendar days as `yyyy-MM-dd` (local time); converted to ISO instants only when calling the API. */
export interface DateRange { from: string; to: string }
export type Bucket = 'hour' | 'day'

export const PRESETS = [7, 30, 90] as const
export type Preset = (typeof PRESETS)[number]

const pad = (n: number) => String(n).padStart(2, '0')
export const toDateInput = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`

/** Range ending today and covering the last `days` calendar days (inclusive). */
export function presetRange(days: number, now = new Date()): DateRange {
  const from = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (days - 1))
  return { from: toDateInput(from), to: toDateInput(now) }
}

const startOfDay = (d: string) => new Date(`${d}T00:00:00`)
const endOfDay = (d: string) => new Date(`${d}T23:59:59.999`)

export function rangeParams(r: DateRange): { from: string; to: string } {
  return { from: startOfDay(r.from).toISOString(), to: endOfDay(r.to).toISOString() }
}

export function isValidRange(r: DateRange): boolean {
  return !!r.from && !!r.to && startOfDay(r.from) <= endOfDay(r.to)
}

/** Hourly buckets for ranges up to two days, daily otherwise. */
export function bucketFor(r: DateRange): Bucket {
  const ms = endOfDay(r.to).getTime() - startOfDay(r.from).getTime()
  return ms <= 2 * 24 * 3600 * 1000 ? 'hour' : 'day'
}

export const isFeatureUnavailable = (err: unknown): boolean => err instanceof HttpError && err.code === 'feature_unavailable'

const key = (name: string, r: DateRange, extra?: string) => ['analytics', name, r.from, r.to, extra ?? ''] as const

export function useOverview(r: DateRange) {
  return useQuery({
    queryKey: key('overview', r),
    queryFn: () => api.get<AnalyticsOverview>('/analytics/overview', rangeParams(r)),
    enabled: isValidRange(r),
    retry: false,
  })
}

export function useTimeseries(r: DateRange, bucket: Bucket) {
  return useQuery({
    queryKey: key('timeseries', r, bucket),
    queryFn: async () => (await api.get<{ items: AnalyticsPoint[] }>('/analytics/timeseries', { ...rangeParams(r), bucket })).items,
    enabled: isValidRange(r),
    retry: false,
  })
}

export function useHeatmap(r: DateRange) {
  return useQuery({
    queryKey: key('heatmap', r),
    queryFn: async () => (await api.get<{ cells: HeatmapCell[] }>('/analytics/heatmap', rangeParams(r))).cells,
    enabled: isValidRange(r),
    retry: false,
  })
}

export function useProfiles(r: DateRange) {
  return useQuery({
    queryKey: key('profiles', r),
    queryFn: async () => (await api.get<{ items: ProfileAnalytics[] }>('/analytics/profiles', rangeParams(r))).items,
    enabled: isValidRange(r),
    retry: false,
  })
}

export function useCampaignAnalytics(r: DateRange) {
  return useQuery({
    queryKey: key('campaigns', r),
    queryFn: async () => (await api.get<{ items: CampaignAnalytics[] }>('/analytics/campaigns', rangeParams(r))).items,
    enabled: isValidRange(r),
    retry: false,
  })
}

/** GET /api/analytics/export.csv with the bearer token, saved through a blob URL. */
export async function downloadAnalyticsCsv(r: DateRange): Promise<void> {
  const p = rangeParams(r)
  const url = `/api/analytics/export.csv?from=${encodeURIComponent(p.from)}&to=${encodeURIComponent(p.to)}`
  const send = () => {
    const token = getToken()
    return fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : {} })
  }
  let res = await send()
  if (res.status === 401 && (await refreshAccessToken())) res = await send()
  if (!res.ok) {
    let code = 'http_error'
    let message = res.statusText
    try {
      const body = (await res.json()) as { error?: { code?: string; message?: string } }
      code = body.error?.code ?? code
      message = body.error?.message ?? message
    } catch { /* not JSON */ }
    throw new HttpError(res.status, code, message)
  }
  const blob = await res.blob()
  const href = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = href
  a.download = `analytics_${r.from}_${r.to}.csv`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(href)
}
