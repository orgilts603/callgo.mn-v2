import { useMemo, useState } from 'react'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { BarChart3 } from 'lucide-react'
import { Card, CardHeader, EmptyState, Skeleton } from '@/components/ui'
import { cn } from '@/lib/utils'
import type { DailyCallCount } from '@/lib/types'
import { fmtDay } from './format'

type Mode = 'direction' | 'outcome'
interface Series { key: keyof Omit<DailyCallCount, 'day'>; label: string; color: string }

const SERIES: Record<Mode, Series[]> = {
  direction: [
    { key: 'inbound', label: 'Ирсэн', color: 'var(--chart-1)' },
    { key: 'outbound', label: 'Гарсан', color: 'var(--chart-2)' },
  ],
  outcome: [
    { key: 'completed', label: 'Амжилттай', color: 'var(--chart-2)' },
    { key: 'failed', label: 'Амжилтгүй', color: 'var(--chart-3)' },
  ],
}
const MODES: { id: Mode; label: string }[] = [
  { id: 'direction', label: 'Чиглэл' },
  { id: 'outcome', label: 'Үр дүн' },
]

interface TooltipEntry { dataKey?: unknown; value?: unknown }
function ChartTooltip({ active, payload, label, series }: { active?: boolean; payload?: ReadonlyArray<TooltipEntry>; label?: unknown; series: Series[] }) {
  if (!active || !payload?.length) return null
  return (
    <div className="min-w-36 rounded-[var(--radius)] border border-[var(--border)] bg-[var(--surface-overlay)] px-3 py-2 text-xs shadow-[var(--shadow-lg)]">
      <div className="mb-1.5 font-medium text-[var(--fg)]">{typeof label === 'string' ? fmtDay(label) : String(label ?? '')}</div>
      {series.map((s) => {
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

const axisProps = {
  stroke: 'var(--chart-axis)',
  tick: { fill: 'var(--chart-axis)', fontSize: 11 },
  tickLine: false,
  axisLine: false,
} as const

export function DailyChart({ data, loading, days = 14 }: { data?: DailyCallCount[]; loading: boolean; days?: number }) {
  const [mode, setMode] = useState<Mode>('direction')
  const series = SERIES[mode]
  const totals = useMemo(() => {
    const t: Record<string, number> = {}
    for (const s of series) t[s.key] = (data ?? []).reduce((acc, d) => acc + (d[s.key] ?? 0), 0)
    return t
  }, [data, series])
  const empty = !loading && (!data || data.length === 0 || data.every((d) => d.inbound + d.outbound + d.completed + d.failed === 0))

  return (
    <Card className="flex flex-col">
      <CardHeader
        title="Дуудлагын динамик"
        description={`Сүүлийн ${days} хоног`}
        actions={
          <div role="tablist" aria-label="Графикийн төрөл" className="flex rounded-[var(--radius-sm)] border border-[var(--border)] bg-[var(--surface-inset)] p-0.5">
            {MODES.map((m) => (
              <button key={m.id} type="button" role="tab" aria-selected={mode === m.id} onClick={() => setMode(m.id)}
                className={cn('h-6 rounded-[4px] px-2.5 text-xs font-medium transition-colors',
                  mode === m.id ? 'bg-[var(--surface-3)] text-[var(--fg)] shadow-[var(--shadow-sm)]' : 'text-[var(--fg-muted)] hover:text-[var(--fg)]')}>
                {m.label}
              </button>
            ))}
          </div>
        }
      />
      <div className="flex items-center gap-5 px-4 pt-3" aria-label="Тайлбар">
        {series.map((s) => (
          <div key={s.key} className="flex items-center gap-2 text-xs">
            <span className="h-2 w-2 rounded-sm" style={{ background: s.color }} aria-hidden />
            <span className="text-[var(--fg-muted)]">{s.label}</span>
            {!loading && <span className="tabular font-medium text-[var(--fg)]">{totals[s.key]}</span>}
          </div>
        ))}
      </div>
      <div className="h-[260px] px-2 pb-3 pt-2">
        {loading ? (
          <div className="flex h-full items-end gap-2 px-3 pb-6">
            {Array.from({ length: 14 }, (_, i) => <Skeleton key={i} className="flex-1" style={{ height: `${25 + ((i * 37) % 60)}%` }} />)}
          </div>
        ) : empty ? (
          <EmptyState icon={<BarChart3 />} title="Мэдээлэл алга" description="Энэ хугацаанд дуудлага бүртгэгдээгүй байна." className="h-full py-0" />
        ) : (
          <ResponsiveContainer width="100%" height="100%">
            {mode === 'direction' ? (
              <AreaChart data={data} margin={{ top: 8, right: 12, bottom: 0, left: -12 }}>
                <defs>
                  {series.map((s) => (
                    <linearGradient key={s.key} id={`fill-${s.key}`} x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor={s.color} stopOpacity={0.22} />
                      <stop offset="100%" stopColor={s.color} stopOpacity={0} />
                    </linearGradient>
                  ))}
                </defs>
                <CartesianGrid vertical={false} stroke="var(--chart-grid)" />
                <XAxis dataKey="day" tickFormatter={fmtDay} {...axisProps} minTickGap={16} />
                <YAxis allowDecimals={false} width={40} {...axisProps} />
                <Tooltip cursor={{ stroke: 'var(--border-strong)', strokeWidth: 1 }}
                  content={(p) => <ChartTooltip active={p.active} payload={p.payload} label={p.label} series={series} />} />
                {series.map((s) => (
                  <Area key={s.key} type="monotone" dataKey={s.key} name={s.label} stroke={s.color} strokeWidth={2}
                    fill={`url(#fill-${s.key})`} dot={false} activeDot={{ r: 4, strokeWidth: 2, stroke: 'var(--surface-1)' }} isAnimationActive={false} />
                ))}
              </AreaChart>
            ) : (
              <BarChart data={data} margin={{ top: 8, right: 12, bottom: 0, left: -12 }} barGap={2} barCategoryGap="28%">
                <CartesianGrid vertical={false} stroke="var(--chart-grid)" />
                <XAxis dataKey="day" tickFormatter={fmtDay} {...axisProps} minTickGap={16} />
                <YAxis allowDecimals={false} width={40} {...axisProps} />
                <Tooltip cursor={{ fill: 'var(--chart-cursor)' }}
                  content={(p) => <ChartTooltip active={p.active} payload={p.payload} label={p.label} series={series} />} />
                {series.map((s) => (
                  <Bar key={s.key} dataKey={s.key} name={s.label} fill={s.color} radius={[4, 4, 0, 0]} maxBarSize={18} isAnimationActive={false} />
                ))}
              </BarChart>
            )}
          </ResponsiveContainer>
        )}
      </div>
    </Card>
  )
}
