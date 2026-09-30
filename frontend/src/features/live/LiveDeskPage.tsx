import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { Activity, BellRing, CheckCircle2, PhoneCall, Timer } from 'lucide-react'
import { Badge, Card, EmptyState, PageHeader, Skeleton, StatCard, type BadgeTone } from '@/components/ui'
import { api } from '@/lib/api'
import { fmtDuration } from '@/lib/utils'
import { useLive } from '@/lib/ws'
import type { CallStats } from '@/lib/types'
import { CallDrawer } from '@/features/calls/CallDrawer'
import { HangupDialog } from '@/features/calls/CallActions'
import { ActiveCallsTable } from './ActiveCallsTable'
import { EventFeed } from './EventFeed'
import type { CallRow } from './types'
import { useActiveCallRows } from './useActiveCallRows'
import { useLiveDeskEvents } from './useLiveDeskEvents'

const FEED_COLLAPSED_KEY = 'callgo.live.feedCollapsed'

function readCollapsed(): boolean {
  try { return localStorage.getItem(FEED_COLLAPSED_KEY) === '1' } catch { return false }
}

const WS_STATUS: Record<'open' | 'connecting' | 'closed', { tone: BadgeTone; label: string }> = {
  open: { tone: 'success', label: 'Шууд холбогдсон' },
  connecting: { tone: 'warning', label: 'Холбогдож байна' },
  closed: { tone: 'danger', label: 'Холболт салсан' },
}

export function WsStatusBadge() {
  const status = useLive((s) => s.status)
  const s = WS_STATUS[status]
  return <Badge tone={s.tone} dot pulse={status !== 'closed'} data-testid="ws-status">{s.label}</Badge>
}

/** Real-time operations desk: KPIs, live calls table, event feed and the call drawer. */
export function LiveDeskPage() {
  const [params, setParams] = useSearchParams()
  const selectedId = params.get('call')
  const [collapsed, setCollapsed] = useState(readCollapsed)
  const [hangupRow, setHangupRow] = useState<CallRow | null>(null)

  useEffect(() => { useLive.getState().connect() }, [])

  const stats = useQuery({ queryKey: ['stats'], queryFn: () => api.get<CallStats>('/stats'), refetchInterval: 60_000 })
  const { meta, feed, endedIds } = useLiveDeskEvents()
  const { rows, isLoading } = useActiveCallRows(meta, endedIds)

  const counts = useMemo(() => {
    let active = 0, ringing = 0, queued = 0
    for (const r of rows) {
      if (r.call.status === 'active') active++
      else if (r.call.status === 'ringing') ringing++
      else if (r.call.status === 'queued') queued++
    }
    return { active, ringing, queued }
  }, [rows])

  const openCall = useCallback((id: string) => {
    setParams((p) => { const n = new URLSearchParams(p); n.set('call', id); return n })
  }, [setParams])
  const closeCall = useCallback(() => {
    setParams((p) => { const n = new URLSearchParams(p); n.delete('call'); return n })
  }, [setParams])
  const toggleFeed = useCallback(() => {
    setCollapsed((c) => {
      try { localStorage.setItem(FEED_COLLAPSED_KEY, c ? '0' : '1') } catch { /* storage unavailable */ }
      return !c
    })
  }, [])

  const s = stats.data
  const statValue = (v: string | number | undefined) => (stats.isLoading ? <Skeleton className="h-7 w-16" /> : v ?? '—')

  return (
    <div>
      <PageHeader title="Live Desk" description="Шууд явагдаж буй дуудлагууд болон AI агентын төлөв" actions={<WsStatusBadge />} />

      <div className="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatCard label="Идэвхтэй дуудлага" value={counts.active} icon={<PhoneCall className="h-4 w-4" />} hint="Одоо ярьж байна" />
        <StatCard label="Дуугарч байгаа" value={counts.ringing + counts.queued} icon={<BellRing className="h-4 w-4" />}
          hint={counts.queued ? `Дараалалд ${counts.queued}` : 'Хариулт хүлээж байна'} />
        <StatCard label="Өнөөдөр дууссан" value={statValue(s?.completedToday)} icon={<CheckCircle2 className="h-4 w-4" />}
          hint={s ? `Ирсэн ${s.inboundToday} · Гарсан ${s.outboundToday}` : undefined} />
        <StatCard label="Дундаж үргэлжлэх хугацаа" value={statValue(s ? fmtDuration(s.avgDurationSec) : undefined)} icon={<Timer className="h-4 w-4" />}
          hint={s ? `Эерэг ${Math.round(s.positiveRatio * 100)}% · Сөрөг ${Math.round(s.negativeRatio * 100)}%` : undefined} />
      </div>

      <div className="flex items-start gap-4">
        <Card className="min-w-0 flex-1 overflow-hidden">
          <div className="flex items-center justify-between border-b border-[var(--border)] px-4 py-3">
            <h3 className="flex items-center gap-2 text-sm font-semibold text-[var(--fg)]">
              <Activity className="h-4 w-4 text-[var(--accent-fg)]" /> Идэвхтэй дуудлагууд
              <span className="rounded-full bg-[var(--surface-2)] px-2 text-xs font-medium tabular-nums text-[var(--fg-muted)]">{rows.length}</span>
            </h3>
          </div>
          {isLoading ? (
            <div className="space-y-2 p-4">{[0, 1, 2].map((i) => <Skeleton key={i} className="h-9 w-full" />)}</div>
          ) : rows.length === 0 ? (
            <EmptyState icon={<PhoneCall className="h-8 w-8" />} title="Одоогоор идэвхтэй дуудлага алга"
              description={<>Шинэ дуудлага ирэхэд энд шууд гарна. Туршихын тулд backend-ийг <code className="rounded bg-[var(--surface-2)] px-1">CALLGO_SIMULATOR=true</code> тохиргоотой ажиллуулна уу.</>} />
          ) : (
            <ActiveCallsTable rows={rows} selectedId={selectedId} onOpen={openCall} onHangup={setHangupRow} />
          )}
        </Card>
        <EventFeed events={feed} collapsed={collapsed} onToggle={toggleFeed} onOpenCall={openCall} />
      </div>

      <CallDrawer callId={selectedId} onClose={closeCall} />
      <HangupDialog call={hangupRow?.call ?? null} onClose={() => setHangupRow(null)} />
    </div>
  )
}

export default LiveDeskPage
