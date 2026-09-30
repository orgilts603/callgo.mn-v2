import { useMemo, useState } from 'react'
import { Area, AreaChart, CartesianGrid, Cell, Pie, PieChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { BarChart3 } from 'lucide-react'
import { Card, CardBody, CardHeader, EmptyState, Skeleton } from '@/components/ui'
import type { AnalyticsOverview, AnalyticsPoint, HeatmapCell } from '@/lib/types'
import { fmtDay, fmtNumber, fmtPercent } from './format'
import type { Bucket } from './hooks'

const axisProps = {
  stroke: 'var(--chart-axis)',
  tick: { fill: 'var(--chart-axis)', fontSize: 11 },
  tickLine: false,
  axisLine: false,
} as const

const tooltipBox = 'min-w-36 rounded-[var(--radius)] border border-[var(--border)] bg-[var(--surface-overlay)] px-3 py-2 text-xs shadow-[var(--shadow-lg)]'

/* ------------------------------------------------------------------ sentiment */

const SENTIMENT = [
  { key: 'positive', label: 'Эерэг', color: 'var(--chart-2)' },
  { key: 'neutral', label: 'Төвийг сахисан', color: 'var(--chart-1)' },
  { key: 'negative', label: 'Сөрөг', color: 'var(--chart-3)' },
] as const

export function SentimentDonut({ sentiment, loading }: { sentiment?: AnalyticsOverview['sentiment']; loading: boolean }) {
  const total = sentiment ? sentiment.positive + sentiment.neutral + sentiment.negative : 0
  const data = SENTIMENT.map((s) => ({ ...s, value: sentiment?.[s.key] ?? 0 }))
  return (
    <Card>
      <CardHeader title="Сэтгэл хандлага" description="Дүгнэлттэй дуудлагууд" />
      <CardBody>
        {loading ? <Skeleton className="mx-auto h-40 w-40 rounded-full" /> : total === 0 ? (
          <EmptyState title="Мэдээлэл алга" className="py-8" />
        ) : (
          <div className="flex flex-wrap items-center justify-center gap-6">
            <div className="relative h-40 w-40" data-testid="sentiment-donut">
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie data={data} dataKey="value" nameKey="label" innerRadius={48} outerRadius={72} paddingAngle={2} stroke="var(--surface-1)" strokeWidth={2} isAnimationActive={false}>
                    {data.map((d) => <Cell key={d.key} fill={d.color} />)}
                  </Pie>
                </PieChart>
              </ResponsiveContainer>
              <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
                <span className="font-mono text-lg font-medium tabular-nums text-[var(--fg)]">{fmtNumber(total)}</span>
                <span className="text-[10px] uppercase tracking-wide text-[var(--fg-subtle)]">дуудлага</span>
              </div>
            </div>
            <ul className="space-y-2 text-xs" aria-label="Сэтгэл хандлагын тайлбар">
              {data.map((d) => (
                <li key={d.key} className="flex items-center gap-2">
                  <span className="h-2 w-2 rounded-sm" style={{ background: d.color }} aria-hidden />
                  <span className="text-[var(--fg-muted)]">{d.label}</span>
                  <span className="tabular ml-auto pl-4 font-medium text-[var(--fg)]">{fmtNumber(d.value)}</span>
                  <span className="tabular w-10 text-right text-[var(--fg-subtle)]">{fmtPercent(d.value / total)}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardBody>
    </Card>
  )
}

/* ------------------------------------------------------------------- outcomes */

export function OutcomesList({ outcomes, loading }: { outcomes?: AnalyticsOverview['outcomes']; loading: boolean }) {
  const rows = useMemo(() => [...(outcomes ?? [])].sort((a, b) => b.count - a.count), [outcomes])
  const max = rows.reduce((m, r) => Math.max(m, r.count), 0)
  return (
    <Card>
      <CardHeader title="Дуудлагын үр дүн" description="Ангилал тус бүрээр" />
      <CardBody>
        {loading ? (
          <div className="space-y-3">{Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-6 w-full" />)}</div>
        ) : rows.length === 0 ? (
          <EmptyState title="Мэдээлэл алга" className="py-8" />
        ) : (
          <ul className="space-y-3" data-testid="outcomes">
            {rows.map((r) => (
              <li key={r.code}>
                <div className="mb-1 flex items-center justify-between gap-3 text-xs">
                  <span className="truncate text-[var(--fg)]">{r.label || r.code}</span>
                  <span className="tabular font-medium text-[var(--fg)]">{fmtNumber(r.count)}</span>
                </div>
                <div className="h-1.5 overflow-hidden rounded-full bg-[var(--surface-2)]">
                  <div className="h-full rounded-full bg-[var(--chart-1)]" style={{ width: `${max ? Math.max(2, (r.count / max) * 100) : 0}%` }} />
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardBody>
    </Card>
  )
}

/* ----------------------------------------------------------------- timeseries */

const pad = (n: number) => String(n).padStart(2, '0')

/** Axis / tooltip label for a bucket start. */
export function fmtBucket(ts: string, bucket: Bucket): string {
  if (bucket === 'day') return fmtDay(ts)
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return ts
  return `${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:00`
}

interface TooltipEntry { dataKey?: unknown; value?: unknown }
const TS_SERIES = [
  { key: 'calls', label: 'Дуудлага', color: 'var(--chart-1)' },
  { key: 'answered', label: 'Хариулсан', color: 'var(--chart-2)' },
] as const

function TsTooltip({ active, payload, label, bucket }: { active?: boolean; payload?: ReadonlyArray<TooltipEntry>; label?: unknown; bucket: Bucket }) {
  if (!active || !payload?.length) return null
  return (
    <div className={tooltipBox}>
      <div className="mb-1.5 font-medium text-[var(--fg)]">{typeof label === 'string' ? fmtBucket(label, bucket) : String(label ?? '')}</div>
      {TS_SERIES.map((s) => {
        const v = payload.find((p) => p.dataKey === s.key)?.value
        return (
          <div key={s.key} className="flex items-center gap-2 py-0.5">
            <span className="h-2 w-2 rounded-sm" style={{ background: s.color }} aria-hidden />
            <span className="text-[var(--fg-muted)]">{s.label}</span>
            <span className="tabular ml-auto pl-4 font-medium text-[var(--fg)]">{typeof v === 'number' ? v : 0}</span>
          </div>
        )
      })}
    </div>
  )
}

export function TimeseriesChart({ points, bucket, loading }: { points?: AnalyticsPoint[]; bucket: Bucket; loading: boolean }) {
  const empty = !loading && (!points || points.every((p) => p.calls === 0))
  return (
    <Card>
      <CardHeader title="Дуудлагын динамик" description={bucket === 'hour' ? 'Цагаар' : 'Өдрөөр'} />
      <div className="flex items-center gap-5 px-4 pt-3" aria-label="Тайлбар">
        {TS_SERIES.map((s) => (
          <div key={s.key} className="flex items-center gap-2 text-xs">
            <span className="h-2 w-2 rounded-sm" style={{ background: s.color }} aria-hidden />
            <span className="text-[var(--fg-muted)]">{s.label}</span>
            {!loading && <span className="tabular font-medium text-[var(--fg)]">{fmtNumber((points ?? []).reduce((a, p) => a + p[s.key], 0))}</span>}
          </div>
        ))}
      </div>
      <div className="h-[260px] px-2 pb-3 pt-2" data-testid="timeseries">
        {loading ? <Skeleton className="mx-3 h-full" /> : empty ? (
          <EmptyState icon={<BarChart3 />} title="Мэдээлэл алга" description="Энэ хугацаанд дуудлага бүртгэгдээгүй байна." className="h-full py-0" />
        ) : (
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={points} margin={{ top: 8, right: 12, bottom: 0, left: -12 }}>
              <defs>
                {TS_SERIES.map((s) => (
                  <linearGradient key={s.key} id={`ts-fill-${s.key}`} x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor={s.color} stopOpacity={0.22} />
                    <stop offset="100%" stopColor={s.color} stopOpacity={0} />
                  </linearGradient>
                ))}
              </defs>
              <CartesianGrid vertical={false} stroke="var(--chart-grid)" />
              <XAxis dataKey="ts" tickFormatter={(v: string) => fmtBucket(v, bucket)} {...axisProps} minTickGap={24} />
              <YAxis allowDecimals={false} width={40} {...axisProps} />
              <Tooltip cursor={{ stroke: 'var(--border-strong)', strokeWidth: 1 }}
                content={(p) => <TsTooltip active={p.active} payload={p.payload} label={p.label} bucket={bucket} />} />
              {TS_SERIES.map((s) => (
                <Area key={s.key} type="monotone" dataKey={s.key} name={s.label} stroke={s.color} strokeWidth={2} fill={`url(#ts-fill-${s.key})`}
                  dot={false} activeDot={{ r: 4, strokeWidth: 2, stroke: 'var(--surface-1)' }} isAnimationActive={false} />
              ))}
            </AreaChart>
          </ResponsiveContainer>
        )}
      </div>
    </Card>
  )
}

/* -------------------------------------------------------------------- heatmap */

/** Display order Monday → Sunday; API weekdays follow Go/JS numbering (0 = Sunday). */
export const WEEKDAY_ORDER = [1, 2, 3, 4, 5, 6, 0] as const
export const WEEKDAY_SHORT: Record<number, string> = { 1: 'Да', 2: 'Мя', 3: 'Лх', 4: 'Пү', 5: 'Ба', 6: 'Бя', 0: 'Ня' }
const WEEKDAY_LONG: Record<number, string> = { 1: 'Даваа', 2: 'Мягмар', 3: 'Лхагва', 4: 'Пүрэв', 5: 'Баасан', 6: 'Бямба', 0: 'Ням' }
const HOURS = Array.from({ length: 24 }, (_, h) => h)

export function cellLabel(weekday: number, hour: number, cell?: HeatmapCell): string {
  const when = `${WEEKDAY_LONG[weekday]} ${pad(hour)}:00`
  if (!cell || cell.calls === 0) return `${when} · дуудлага байхгүй`
  return `${when} · ${fmtNumber(cell.calls)} дуудлага · ${fmtPercent(cell.answerRate)} хариулсан`
}

export function Heatmap({ cells, loading }: { cells?: HeatmapCell[]; loading: boolean }) {
  const [hover, setHover] = useState<string | null>(null)
  const { byKey, max } = useMemo(() => {
    const m = new Map<string, HeatmapCell>()
    let mx = 0
    for (const c of cells ?? []) { m.set(`${c.weekday}-${c.hour}`, c); mx = Math.max(mx, c.calls) }
    return { byKey: m, max: mx }
  }, [cells])
  return (
    <Card>
      <CardHeader title="Цагийн ачаалал" description="Долоо хоногийн өдөр × цаг (байгууллагын цагийн бүс)" />
      <CardBody>
        {loading ? <Skeleton className="h-52 w-full" /> : max === 0 ? (
          <EmptyState title="Мэдээлэл алга" className="py-8" />
        ) : (
          <div className="overflow-x-auto">
            <div className="min-w-[640px]" data-testid="heatmap" onMouseLeave={() => setHover(null)}>
              <div className="grid gap-[3px]" style={{ gridTemplateColumns: '28px repeat(24, minmax(0, 1fr))' }}>
                <span />
                {HOURS.map((h) => (
                  <span key={h} className="text-center text-[10px] tabular-nums text-[var(--fg-subtle)]">{h % 3 === 0 ? pad(h) : ''}</span>
                ))}
                {WEEKDAY_ORDER.map((wd) => (
                  <HeatRow key={wd} weekday={wd} byKey={byKey} max={max} onHover={setHover} />
                ))}
              </div>
              <div className="mt-3 flex items-center justify-between gap-4 text-[11px] text-[var(--fg-muted)]">
                <span aria-live="polite" data-testid="heatmap-tip" className="min-h-4">{hover ?? 'Нүд дээр хулганаа аваачиж дэлгэрэнгүйг харна уу'}</span>
                <span className="flex shrink-0 items-center gap-1.5" aria-hidden>
                  <span>Цөөн</span>
                  {[8, 30, 55, 80, 100].map((p) => (
                    <span key={p} className="h-2.5 w-4 rounded-[2px]" style={{ background: shade(p) }} />
                  ))}
                  <span>Олон</span>
                </span>
              </div>
            </div>
          </div>
        )}
      </CardBody>
    </Card>
  )
}

const shade = (pct: number) => `color-mix(in srgb, var(--chart-2) ${pct}%, transparent)`

function HeatRow({ weekday, byKey, max, onHover }: { weekday: number; byKey: Map<string, HeatmapCell>; max: number; onHover: (s: string | null) => void }) {
  return (
    <>
      <span className="flex items-center text-[10px] text-[var(--fg-subtle)]">{WEEKDAY_SHORT[weekday]}</span>
      {HOURS.map((h) => {
        const cell = byKey.get(`${weekday}-${h}`)
        const label = cellLabel(weekday, h, cell)
        const calls = cell?.calls ?? 0
        return (
          <div key={h} data-testid="heat-cell" data-calls={calls} title={label} aria-label={label} role="img"
            onMouseEnter={() => onHover(label)}
            className="aspect-square min-h-3 rounded-[3px] border border-[var(--border-subtle)]"
            style={{ background: calls > 0 ? shade(12 + (calls / max) * 88) : 'var(--surface-2)' }} />
        )
      })}
    </>
  )
}
