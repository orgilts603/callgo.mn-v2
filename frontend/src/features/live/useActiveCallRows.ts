import { useCallback, useMemo, useState } from 'react'
import { useQueries, useQuery, type UseQueryResult } from '@tanstack/react-query'
import { useLive } from '@/lib/ws'
import type { Call } from '@/lib/types'
import { activeCallsKey, callsApi, contactNameKey, isLiveStatus, useCampaignNames } from '@/features/calls/api'
import type { CallLiveMeta, CallRow } from './types'

const NO_CALLS: Call[] = []
const selectContactName = (r: { contact: { name: string } }) => r.contact.name

function newer(a: Call, b: Call): Call {
  return (Date.parse(b.updatedAt) || 0) >= (Date.parse(a.updatedAt) || 0) ? b : a
}

/** Merge the REST snapshot with the socket store; the socket wins unless the snapshot is newer. */
export function mergeActiveCalls(fetched: readonly Call[], store: Record<string, Call>, endedIds: ReadonlySet<string>): Call[] {
  const map = new Map<string, Call>()
  for (const c of fetched) if (isLiveStatus(c.status) && !endedIds.has(c.id)) map.set(c.id, c)
  for (const c of Object.values(store)) {
    if (endedIds.has(c.id) || !isLiveStatus(c.status)) continue
    const prev = map.get(c.id)
    map.set(c.id, prev ? newer(prev, c) : c)
  }
  return [...map.values()]
}

function metadataName(call: Call): string | null {
  const v = call.metadata?.contactName ?? call.metadata?.name
  return typeof v === 'string' && v ? v : null
}

function sameRow(a: CallRow, b: CallRow): boolean {
  return a.call === b.call && a.contactName === b.contactName && a.campaignName === b.campaignName
    && a.agentState === b.agentState && a.snippet === b.snippet
}

/** Live Desk rows: merged active calls enriched with contact/campaign names and live meta. */
export function useActiveCallRows(meta: Record<string, CallLiveMeta>, endedIds: ReadonlySet<string>) {
  const storeCalls = useLive((s) => s.activeCalls)
  const activeQ = useQuery({ queryKey: activeCallsKey, queryFn: callsApi.active, refetchInterval: 30_000 })
  const fetched = activeQ.data?.items ?? NO_CALLS

  const calls = useMemo(() => mergeActiveCalls(fetched, storeCalls, endedIds), [fetched, storeCalls, endedIds])

  const campaignNames = useCampaignNames(calls.some((c) => !!c.campaignId))

  const contactIds = useMemo(() => {
    const ids = new Set<string>()
    for (const c of calls) if (c.contactId && !metadataName(c)) ids.add(c.contactId)
    return [...ids].sort()
  }, [calls])
  const combine = useCallback((results: UseQueryResult<string, Error>[]) => {
    const names: Record<string, string> = {}
    results.forEach((r, i) => { if (r.data) names[contactIds[i]] = r.data })
    return names
  }, [contactIds])
  const contactNames = useQueries({
    queries: contactIds.map((id) => ({
      queryKey: contactNameKey(id),
      queryFn: () => callsApi.contact(id),
      staleTime: 5 * 60_000,
      select: selectContactName,
    })),
    combine,
  })

  // Reuse row objects whose inputs did not change so memoized table rows skip rendering.
  // Instance-scoped memo of the previous rows (idempotent, so safe under StrictMode double render).
  const [cache] = useState(() => new Map<string, CallRow>())
  const rows = useMemo(() => {
    const out: CallRow[] = []
    for (const call of calls) {
      const m = meta[call.id]
      const candidate: CallRow = {
        id: call.id,
        call,
        contactName: metadataName(call) ?? (call.contactId ? contactNames[call.contactId] ?? null : null),
        campaignName: call.campaignId ? campaignNames.get(call.campaignId) ?? null : null,
        agentState: m?.agentState ?? null,
        snippet: m?.snippet ?? null,
      }
      const prev = cache.get(call.id)
      const row = prev && sameRow(prev, candidate) ? prev : candidate
      out.push(row)
    }
    cache.clear()
    for (const r of out) cache.set(r.id, r)
    return out
  }, [calls, meta, contactNames, campaignNames, cache])

  return { rows, isLoading: activeQ.isLoading && calls.length === 0, error: activeQ.error }
}
