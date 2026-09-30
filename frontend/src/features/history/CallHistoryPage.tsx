import { lazy, Suspense, useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowDownLeft, ArrowUpRight, ChevronLeft, ChevronRight, Download, Mic, Search } from 'lucide-react'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import { cn, fmtDateTime, fmtDuration, fmtPhone } from '@/lib/utils'
import type { Call, CallDirection, CallStatus, Campaign, ListResponse } from '@/lib/types'
import {
  Button, Card, CallStatusBadge, EmptyState, Input, PageHeader, SentimentBadge, Select, Skeleton, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import { callsToCsv, downloadCsv } from './csv'
import { useDebounced } from './useDebounced'

const CallDrawer = lazy(() => import('@/features/calls/CallDrawer').then((m) => ({ default: m.CallDrawer })))

export const PAGE_SIZE = 25

const STATUS_OPTIONS: { value: CallStatus; label: string }[] = [
  { value: 'queued', label: 'Дараалалд' },
  { value: 'ringing', label: 'Дуугарч байна' },
  { value: 'active', label: 'Ярьж байна' },
  { value: 'completed', label: 'Дууссан' },
  { value: 'failed', label: 'Амжилтгүй' },
  { value: 'no_answer', label: 'Хариулаагүй' },
  { value: 'busy', label: 'Завгүй' },
  { value: 'voicemail', label: 'Дуут шуудан' },
]

function startOfDay(d: string): string | undefined { return d ? new Date(`${d}T00:00:00`).toISOString() : undefined }
function endOfDay(d: string): string | undefined { return d ? new Date(`${d}T23:59:59.999`).toISOString() : undefined }

export function CallHistoryPage() {
  const qc = useQueryClient()
  const [params, setParams] = useSearchParams()
  const callId = params.get('call')

  const [search, setSearch] = useState('')
  const [statuses, setStatuses] = useState<CallStatus[]>([])
  const [direction, setDirection] = useState<'' | CallDirection>('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [campaignId, setCampaignId] = useState('')
  const [page, setPage] = useState(0)
  const q = useDebounced(search.trim(), 300)

  useEffect(() => { setPage(0) }, [q, statuses, direction, from, to, campaignId])

  const query = useMemo(() => ({
    status: statuses.length ? statuses.join(',') : undefined,
    direction: direction || undefined,
    campaignId: campaignId || undefined,
    q: q || undefined,
    from: startOfDay(from),
    to: endOfDay(to),
    limit: PAGE_SIZE,
    offset: page * PAGE_SIZE,
  }), [statuses, direction, campaignId, q, from, to, page])

  const calls = useQuery({
    queryKey: ['calls', 'history', query],
    queryFn: () => api.get<ListResponse<Call>>('/calls', query),
    placeholderData: keepPreviousData,
  })
  const campaigns = useQuery({
    queryKey: ['campaigns', 'options'],
    queryFn: () => api.get<{ items: Campaign[] }>('/campaigns'),
  })
  const campaignNames = useMemo(
    () => Object.fromEntries((campaigns.data?.items ?? []).map((c) => [c.id, c.name])),
    [campaigns.data],
  )

  useEffect(() => {
    return useLive.getState().onEvent((ev) => {
      if (ev.type === 'call.ended') void qc.invalidateQueries({ queryKey: ['calls', 'history'] })
    })
  }, [qc])

  const openCall = useCallback((id: string) => {
    setParams((p) => { const n = new URLSearchParams(p); n.set('call', id); return n }, { replace: false })
  }, [setParams])
  const closeCall = useCallback(() => {
    setParams((p) => { const n = new URLSearchParams(p); n.delete('call'); return n }, { replace: true })
  }, [setParams])

  const toggleStatus = (s: CallStatus) =>
    setStatuses((cur) => (cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]))

  const items = calls.data?.items ?? []
  const total = calls.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const filtered = search || statuses.length > 0 || direction || from || to || campaignId

  const exportCsv = () => downloadCsv(`calls-${new Date().toISOString().slice(0, 10)}.csv`, callsToCsv(items, campaignNames))

  return (
    <div>
      <PageHeader
        title="Дуудлагын түүх"
        description="Бүх ирсэн болон гарсан дуудлага"
        actions={<Button variant="secondary" size="sm" onClick={exportCsv} disabled={items.length === 0}><Download className="h-4 w-4" />CSV татах</Button>}
      />

      <Card className="mb-4 space-y-3 p-4">
        <div className="flex flex-wrap items-center gap-3">
          <div className="relative min-w-56 flex-1">
            <Search className="pointer-events-none absolute left-3 top-2.5 h-4 w-4 text-[var(--fg-subtle)]" />
            <Input
              className="pl-9" placeholder="Утас, нэр, хураангуйгаар хайх" aria-label="Хайх"
              value={search} onChange={(e) => setSearch(e.target.value)}
            />
          </div>
          <Select className="w-40" aria-label="Чиглэл" value={direction} onChange={(e) => setDirection(e.target.value as '' | CallDirection)}
            options={[{ value: 'inbound', label: 'Ирсэн' }, { value: 'outbound', label: 'Гарсан' }]} placeholder="Бүх чиглэл" />
          <Select className="w-48" aria-label="Кампанит ажил" value={campaignId} onChange={(e) => setCampaignId(e.target.value)}
            options={(campaigns.data?.items ?? []).map((c) => ({ value: c.id, label: c.name }))} placeholder="Бүх кампанит ажил" />
          <div className="flex items-center gap-2">
            <Input type="date" className="w-40" aria-label="Эхлэх огноо" value={from} onChange={(e) => setFrom(e.target.value)} />
            <span className="text-[var(--fg-subtle)]">—</span>
            <Input type="date" className="w-40" aria-label="Дуусах огноо" value={to} onChange={(e) => setTo(e.target.value)} />
          </div>
        </div>
        <div className="flex flex-wrap gap-2" role="group" aria-label="Төлөв">
          {STATUS_OPTIONS.map((s) => {
            const on = statuses.includes(s.value)
            return (
              <button
                key={s.value} type="button" aria-pressed={on} onClick={() => toggleStatus(s.value)}
                className={cn(
                  'cursor-pointer rounded-full border px-3 py-1 text-xs font-medium transition-colors',
                  on ? 'border-[var(--accent)] bg-[var(--accent)]/15 text-[var(--accent-fg)]' : 'border-[var(--border)] text-[var(--fg-muted)] hover:bg-[var(--surface-2)]',
                )}
              >{s.label}</button>
            )
          })}
        </div>
      </Card>

      <Card>
        {calls.isLoading ? (
          <div className="space-y-2 p-4">{Array.from({ length: 6 }, (_, i) => <Skeleton key={i} className="h-9 w-full" />)}</div>
        ) : calls.isError ? (
          <EmptyState title="Дуудлагын жагсаалт ачаалж чадсангүй" description={(calls.error as Error).message}
            action={<Button variant="secondary" size="sm" onClick={() => void calls.refetch()}>Дахин оролдох</Button>} />
        ) : items.length === 0 ? (
          <EmptyState title="Дуудлага олдсонгүй" description={filtered ? 'Шүүлтүүрээ өөрчилж үзнэ үү' : 'Дуудлага хийгдэх үед энд харагдана'} />
        ) : (
          <Table>
            <THead>
              <tr>
                <TH>Эхэлсэн</TH><TH className="w-10" /><TH>Хаанаас</TH><TH>Хаашаа</TH><TH>Харилцагч / Кампанит ажил</TH>
                <TH>Төлөв</TH><TH>Үргэлжлэх</TH><TH>Сэтгэгдэл</TH><TH>Хураангуй</TH><TH className="w-8" />
              </tr>
            </THead>
            <TBody>
              {items.map((c) => (
                <TR key={c.id} className="cursor-pointer" onClick={() => openCall(c.id)} data-testid="call-row">
                  <TD className="whitespace-nowrap tabular-nums">{fmtDateTime(c.startedAt)}</TD>
                  <TD>
                    {c.direction === 'inbound'
                      ? <ArrowDownLeft className="h-4 w-4 text-emerald-400" aria-label="Ирсэн" />
                      : <ArrowUpRight className="h-4 w-4 text-sky-400" aria-label="Гарсан" />}
                  </TD>
                  <TD className="whitespace-nowrap tabular-nums">{fmtPhone(c.fromNumber)}</TD>
                  <TD className="whitespace-nowrap tabular-nums">{fmtPhone(c.toNumber)}</TD>
                  <TD className="text-[var(--fg-muted)]">
                    {c.campaignId ? (campaignNames[c.campaignId] ?? 'Кампанит ажил') : c.contactId ? 'Харилцагч' : '—'}
                  </TD>
                  <TD><CallStatusBadge status={c.status} /></TD>
                  <TD className="tabular-nums">{fmtDuration(c.durationSec)}</TD>
                  <TD><SentimentBadge sentiment={c.sentiment} /></TD>
                  <TD className="max-w-64 truncate text-[var(--fg-muted)]" title={c.summary}>{c.summary || '—'}</TD>
                  <TD>{c.recordingUrl && <Mic className="h-4 w-4 text-[var(--fg-muted)]" aria-label="Бичлэгтэй" />}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
        <div className="flex items-center justify-between border-t border-[var(--border)] px-4 py-3 text-xs text-[var(--fg-muted)]">
          <span>Нийт {total} дуудлага</span>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="icon" className="h-8 w-8" aria-label="Өмнөх" disabled={page === 0} onClick={() => setPage((p) => Math.max(0, p - 1))}><ChevronLeft className="h-4 w-4" /></Button>
            <span className="tabular-nums">{page + 1} / {pages}</span>
            <Button variant="outline" size="icon" className="h-8 w-8" aria-label="Дараах" disabled={page + 1 >= pages} onClick={() => setPage((p) => p + 1)}><ChevronRight className="h-4 w-4" /></Button>
          </div>
        </div>
      </Card>

      {callId && (
        <Suspense fallback={null}>
          <CallDrawer callId={callId} onClose={closeCall} />
        </Suspense>
      )}
    </div>
  )
}
