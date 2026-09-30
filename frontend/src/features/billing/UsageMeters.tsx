import type { ReactNode } from 'react'
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { AlertTriangle, BarChart3, Brain, MessageSquare, Mic, PhoneCall, Volume2 } from 'lucide-react'
import { Card, CardBody, CardHeader, EmptyState, Skeleton, StatCard } from '@/components/ui'
import { cn } from '@/lib/utils'
import type { Plan, UsageSummary } from '@/lib/types'
import { useSubscription, useUsage, type DailyUsage } from './hooks'
import { fmtLimit, fmtMnt, fmtNum, fmtPeriod, meterBarClass, meterTone, usagePercent } from './format'
import { errMsg } from './errors'

export function UsageMeter({ label, used, included, unit, footer }: { label: string; used: number; included: number; unit: string; footer?: ReactNode }) {
  const unlimited = included <= 0
  const pct = usagePercent(used, included)
  const tone = meterTone(pct)
  return (
    <div data-testid="usage-meter" data-tone={unlimited ? 'none' : tone}>
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-[13px] font-medium text-[var(--fg)]">{label}</span>
        <span className="font-mono text-[13px] tabular-nums text-[var(--fg)]">
          {fmtNum(used, 1)} <span className="text-[var(--fg-muted)]">/ {unlimited ? 'хязгааргүй' : `${fmtNum(included)} ${unit}`}</span>
        </span>
      </div>
      <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-[var(--surface-3)]" role="progressbar" aria-label={label}
        aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(Math.min(pct, 100))}>
        {!unlimited && <div data-testid="usage-meter-bar" className={cn('h-full rounded-full transition-[width]', meterBarClass[tone])} style={{ width: `${Math.min(100, pct)}%` }} />}
      </div>
      <div className="mt-1.5 flex justify-between text-xs text-[var(--fg-muted)]">
        <span>{unlimited ? '' : `${fmtNum(pct, 0)}% ашигласан`}</span>
        {footer}
      </div>
    </div>
  )
}

const axisProps = { stroke: 'var(--chart-axis)', tick: { fill: 'var(--chart-axis)', fontSize: 11 }, tickLine: false, axisLine: false } as const
const fmtDay = (d: unknown) => (typeof d === 'string' ? d.slice(5, 10) : String(d ?? ''))

interface TooltipEntry { dataKey?: unknown; value?: unknown; payload?: unknown }
function UsageTooltip({ active, payload, label }: { active?: boolean; payload?: ReadonlyArray<TooltipEntry>; label?: unknown }) {
  if (!active || !payload?.length) return null
  const row = payload[0]?.payload as DailyUsage | undefined
  return (
    <div className="min-w-36 rounded-[var(--radius)] border border-[var(--border)] bg-[var(--surface-overlay)] px-3 py-2 text-xs shadow-[var(--shadow-lg)]">
      <div className="mb-1.5 font-medium text-[var(--fg)]">{typeof label === 'string' ? label.slice(0, 10) : ''}</div>
      <Line color="var(--chart-2)" label="Минут" value={fmtNum(row?.minutes ?? 0, 1)} />
      <Line color="var(--chart-1)" label="Дуудлага" value={fmtNum(row?.calls ?? 0)} />
    </div>
  )
}
function Line({ color, label, value }: { color: string; label: string; value: string }) {
  return (
    <div className="flex items-center gap-2 py-0.5">
      <span className="h-2 w-2 rounded-sm" style={{ background: color }} aria-hidden />
      <span className="text-[var(--fg-muted)]">{label}</span>
      <span className="ml-auto pl-4 font-medium tabular-nums text-[var(--fg)]">{value}</span>
    </div>
  )
}

function DailyUsageChart({ from, to }: { from: string; to: string }) {
  const usage = useUsage(from, to)
  const data = usage.data?.daily ?? []
  const empty = !usage.isLoading && data.every((d) => d.minutes === 0 && d.calls === 0)
  return (
    <Card>
      <CardHeader title="Өдөр тутмын хэрэглээ" description={fmtPeriod(from, to)} actions={
        <div className="flex items-center gap-4 text-xs text-[var(--fg-muted)]">
          <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-sm bg-[var(--chart-2)]" aria-hidden />Минут</span>
          <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-sm bg-[var(--chart-1)]" aria-hidden />Дуудлага</span>
        </div>
      } />
      <div className="h-[240px] px-2 pb-3 pt-2">
        {usage.isLoading ? <Skeleton className="h-full" />
          : usage.isError ? <EmptyState icon={<AlertTriangle />} title="Хэрэглээг ачаалж чадсангүй" description={errMsg(usage.error)} className="h-full py-0" />
          : empty ? <EmptyState icon={<BarChart3 />} title="Мэдээлэл алга" description="Энэ тооцооны үед хэрэглээ бүртгэгдээгүй байна." className="h-full py-0" />
          : (
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={data} margin={{ top: 8, right: 12, bottom: 0, left: -12 }} barGap={2} barCategoryGap="28%">
                <CartesianGrid vertical={false} stroke="var(--chart-grid)" />
                <XAxis dataKey="day" tickFormatter={fmtDay} {...axisProps} minTickGap={16} />
                <YAxis allowDecimals={false} width={40} {...axisProps} />
                <Tooltip cursor={{ fill: 'var(--chart-cursor)' }} content={(p) => <UsageTooltip active={p.active} payload={p.payload} label={p.label} />} />
                <Bar dataKey="minutes" name="Минут" fill="var(--chart-2)" radius={[4, 4, 0, 0]} maxBarSize={18} isAnimationActive={false} />
                <Bar dataKey="calls" name="Дуудлага" fill="var(--chart-1)" radius={[4, 4, 0, 0]} maxBarSize={18} isAnimationActive={false} />
              </BarChart>
            </ResponsiveContainer>
          )}
      </div>
    </Card>
  )
}

function LimitRow({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-[var(--border-subtle)] py-2 text-[13px] last:border-0">
      <span className="text-[var(--fg-muted)]">{k}</span><span className="tabular-nums text-[var(--fg)]">{v}</span>
    </div>
  )
}

export function UsageMetersView({ usage, limits }: { usage: UsageSummary; limits: Plan }) {
  const included = usage.includedMinutes > 0 ? usage.includedMinutes : limits.includedMinutes
  const overage = usage.overageMinutes > 0 || usage.overageMnt > 0
  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <Card className="lg:col-span-2">
        <CardHeader title="Ярианы минут" description={`Тооцооны үе: ${fmtPeriod(usage.periodStart, usage.periodEnd)}`} />
        <CardBody className="space-y-4">
          <UsageMeter label="Багцад багтсан минут" used={usage.minutes} included={included} unit="мин"
            footer={overage ? <span className="text-[var(--danger-fg)]">Нэмэлт: {fmtNum(usage.overageMinutes, 1)} мин · {fmtMnt(usage.overageMnt)}</span> : null} />
          {limits.overageMntPerMin > 0
            ? <p className="text-xs text-[var(--fg-subtle)]">Багцаас хэтэрсэн минут бүрд {fmtMnt(limits.overageMntPerMin)} (НӨАТ ороогүй) нэмэгдэнэ.</p>
            : <p className="text-xs text-[var(--fg-subtle)]">Энэ багцад нэмэлт минут зөвшөөрөгдөхгүй — хязгаарт хүрэхэд шинэ дуудлага хаагдана.</p>}
        </CardBody>
      </Card>
      <Card>
        <CardHeader title="Багцын хязгаар" description={limits.name} />
        <CardBody className="py-2">
          <LimitRow k="Зэрэг дуудлага" v={fmtLimit(limits.maxConcurrentCalls)} />
          <LimitRow k="Агент профайл" v={fmtLimit(limits.maxAgentProfiles)} />
          <LimitRow k="Хэрэглэгч" v={fmtLimit(limits.maxUsers)} />
          <LimitRow k="SIP дугаар" v={fmtLimit(limits.maxSipNumbers)} />
          <LimitRow k="Мэдлэгийн сан" v={fmtLimit(limits.maxKnowledgeMb, 'MB')} />
        </CardBody>
      </Card>
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-5 lg:col-span-3">
        <StatCard label="Дуудлага" value={fmtNum(usage.calls)} icon={<PhoneCall />} />
        <StatCard label="LLM токен" value={fmtNum(usage.llmTokens)} icon={<Brain />} />
        <StatCard label="STT минут" value={fmtNum(usage.sttSeconds / 60, 1)} icon={<Mic />} />
        <StatCard label="TTS тэмдэгт" value={fmtNum(usage.ttsChars)} icon={<Volume2 />} />
        <StatCard label="SMS" value={fmtNum(usage.sms)} icon={<MessageSquare />} />
      </div>
    </div>
  )
}

export function UsageMeters() {
  const sub = useSubscription()
  if (sub.isLoading) return <div className="grid gap-4 lg:grid-cols-3"><Skeleton className="h-44 lg:col-span-2" /><Skeleton className="h-44" /></div>
  if (sub.isError || !sub.data) {
    return <Card><EmptyState icon={<AlertTriangle />} title="Хэрэглээг ачаалж чадсангүй" description={errMsg(sub.error)} /></Card>
  }
  const { usage, limits, subscription } = sub.data
  return (
    <div className="space-y-4">
      <UsageMetersView usage={usage} limits={limits} />
      <DailyUsageChart from={usage.periodStart || subscription.currentPeriodStart} to={usage.periodEnd || subscription.currentPeriodEnd} />
    </div>
  )
}
