import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { Clock, Coins, Download, Hourglass, PhoneCall, PhoneIncoming, Receipt, Sparkles } from 'lucide-react'
import {
  Button, Card, CardHeader, EmptyState, Input, PageHeader, Skeleton, StatCard, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import { cn, fmtDuration } from '@/lib/utils'
import type { CampaignAnalytics, ProfileAnalytics } from '@/lib/types'
import { Heatmap, OutcomesList, SentimentDonut, TimeseriesChart } from './charts'
import { fmtMinutes, fmtMnt, fmtNumber, fmtPercent } from './format'
import {
  bucketFor, downloadAnalyticsCsv, isFeatureUnavailable, isValidRange, presetRange, PRESETS,
  useCampaignAnalytics, useHeatmap, useOverview, useProfiles, useTimeseries, type DateRange,
} from './hooks'

type Mode = (typeof PRESETS)[number] | 'custom'

function UpgradeCard() {
  return (
    <Card className="mx-auto max-w-lg" data-testid="analytics-upgrade">
      <EmptyState icon={<Sparkles />} title="Аналитик таны багцад ороогүй байна"
        description="Дэлгэрэнгүй тайлан, цагийн ачаалал, зардлын шинжилгээг ашиглахын тулд багцаа шинэчилнэ үү."
        action={<Link to="/settings/billing" className="inline-flex h-8 items-center rounded-[var(--radius-sm)] bg-[var(--accent)] px-3 text-[13px] font-medium text-[var(--fg-on-accent)] hover:bg-[var(--accent-hover)]">Багц сонгох</Link>} />
    </Card>
  )
}

function RangePicker({ mode, range, onPreset, onCustom }: { mode: Mode; range: DateRange; onPreset: (d: (typeof PRESETS)[number]) => void; onCustom: (r: DateRange) => void }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <div role="group" aria-label="Хугацааны сонголт" className="flex rounded-[var(--radius-sm)] border border-[var(--border)] bg-[var(--surface-inset)] p-0.5">
        {PRESETS.map((d) => (
          <button key={d} type="button" aria-pressed={mode === d} onClick={() => onPreset(d)}
            className={cn('h-6 rounded-[4px] px-2.5 text-xs font-medium transition-colors', mode === d ? 'bg-[var(--surface-3)] text-[var(--fg)] shadow-[var(--shadow-sm)]' : 'text-[var(--fg-muted)] hover:text-[var(--fg)]')}>
            {d} хоног
          </button>
        ))}
        <button type="button" aria-pressed={mode === 'custom'} onClick={() => onCustom(range)}
          className={cn('h-6 rounded-[4px] px-2.5 text-xs font-medium transition-colors', mode === 'custom' ? 'bg-[var(--surface-3)] text-[var(--fg)] shadow-[var(--shadow-sm)]' : 'text-[var(--fg-muted)] hover:text-[var(--fg)]')}>
          Сонгох
        </button>
      </div>
      {mode === 'custom' && (
        <div className="flex items-center gap-1.5">
          <Input type="date" className="h-7 w-36" aria-label="Эхлэх огноо" value={range.from} max={range.to || undefined}
            onChange={(e) => onCustom({ ...range, from: e.target.value })} />
          <span className="text-xs text-[var(--fg-subtle)]">—</span>
          <Input type="date" className="h-7 w-36" aria-label="Дуусах огноо" value={range.to} min={range.from || undefined}
            onChange={(e) => onCustom({ ...range, to: e.target.value })} />
        </div>
      )}
    </div>
  )
}

function ProfilesTable({ rows, loading }: { rows?: ProfileAnalytics[]; loading: boolean }) {
  return (
    <Card className="overflow-hidden">
      <CardHeader title="Агентын профайлууд" description="Профайл тус бүрийн гүйцэтгэл" />
      {loading ? <div className="space-y-2 p-4"><Skeleton className="h-8" /><Skeleton className="h-8" /></div> : !rows?.length ? (
        <EmptyState title="Мэдээлэл алга" className="py-8" />
      ) : (
        <Table aria-label="Агентын профайлын аналитик">
          <THead><TR><TH>Профайл</TH><TH className="text-right">Дуудлага</TH><TH className="text-right">Хариулсан</TH><TH className="text-right">Дундаж</TH><TH className="text-right">Эерэг</TH><TH className="text-right">Зардал</TH></TR></THead>
          <TBody>
            {rows.map((p) => (
              <TR key={p.profileId}>
                <TD className="font-medium">{p.name}</TD>
                <TD className="tabular text-right">{fmtNumber(p.calls)}</TD>
                <TD className="tabular text-right">{fmtPercent(p.answerRate)}</TD>
                <TD className="tabular text-right">{fmtDuration(p.avgDurationSec)}</TD>
                <TD className="tabular text-right">{fmtPercent(p.positiveRate)}</TD>
                <TD className="tabular text-right">{fmtMnt(p.costMnt)}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </Card>
  )
}

function CampaignsTable({ rows, loading }: { rows?: CampaignAnalytics[]; loading: boolean }) {
  return (
    <Card className="overflow-hidden">
      <CardHeader title="Кампанит ажлууд" description="Дуудлагын явц ба зардал" />
      {loading ? <div className="space-y-2 p-4"><Skeleton className="h-8" /><Skeleton className="h-8" /></div> : !rows?.length ? (
        <EmptyState title="Мэдээлэл алга" className="py-8" />
      ) : (
        <Table aria-label="Кампанит ажлын аналитик">
          <THead><TR><TH>Кампанит ажил</TH><TH className="text-right">Нийт</TH><TH className="text-right">Дууссан</TH><TH className="text-right">Амжилтгүй</TH><TH className="text-right">Алгассан</TH><TH className="text-right">Минут</TH><TH className="text-right">Зардал</TH></TR></THead>
          <TBody>
            {rows.map((c) => (
              <TR key={c.campaignId}>
                <TD className="font-medium"><Link to={`/campaigns/${c.campaignId}`} className="hover:underline">{c.name}</Link></TD>
                <TD className="tabular text-right">{fmtNumber(c.total)}</TD>
                <TD className="tabular text-right">{fmtNumber(c.done)}</TD>
                <TD className="tabular text-right">{fmtNumber(c.failed)}</TD>
                <TD className="tabular text-right">{fmtNumber(c.skipped)}</TD>
                <TD className="tabular text-right">{fmtMinutes(c.minutes)}</TD>
                <TD className="tabular text-right">{fmtMnt(c.costMnt)}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </Card>
  )
}

export default function AnalyticsPage() {
  const [mode, setMode] = useState<Mode>(30)
  const [range, setRange] = useState<DateRange>(() => presetRange(30))
  const [exporting, setExporting] = useState(false)
  const valid = isValidRange(range)
  const bucket = useMemo(() => bucketFor(range), [range])

  const overview = useOverview(range)
  const series = useTimeseries(range, bucket)
  const heat = useHeatmap(range)
  const profiles = useProfiles(range)
  const campaigns = useCampaignAnalytics(range)

  const pickPreset = (d: (typeof PRESETS)[number]) => { setMode(d); setRange(presetRange(d)) }
  const pickCustom = (r: DateRange) => { setMode('custom'); setRange(r) }

  const exportCsv = async () => {
    setExporting(true)
    try {
      await downloadAnalyticsCsv(range)
    } catch (err) {
      toast.error(isFeatureUnavailable(err) ? 'Аналитик таны багцад ороогүй байна' : (err as Error).message || 'CSV татаж чадсангүй')
    } finally {
      setExporting(false)
    }
  }

  const header = (
    <PageHeader title="Аналитик" description="Дуудлагын гүйцэтгэл, зардал, сэтгэл хандлагын тайлан"
      actions={
        <>
          <RangePicker mode={mode} range={range} onPreset={pickPreset} onCustom={pickCustom} />
          <Button variant="outline" size="sm" onClick={() => void exportCsv()} loading={exporting} disabled={!valid}>
            <Download /> CSV татах
          </Button>
        </>
      } />
  )

  if (isFeatureUnavailable(overview.error)) {
    return <div className="space-y-6">{header}<UpgradeCard /></div>
  }

  const o = overview.data
  const ol = overview.isLoading
  return (
    <div className="space-y-6">
      {header}
      {!valid && <p className="text-xs text-[var(--danger-fg)]" role="alert">Хугацааны сонголт буруу байна.</p>}
      {overview.isError && !isFeatureUnavailable(overview.error) && (
        <p className="text-xs text-[var(--danger-fg)]" role="alert">{overview.error.message || 'Аналитик ачаалж чадсангүй'}</p>
      )}

      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6" data-testid="analytics-stats">
        <StatCard label="Дуудлага" icon={<PhoneCall />} value={ol ? '…' : fmtNumber(o?.calls)}
          hint={o ? `Ирсэн ${fmtNumber(o.byDirection.inbound)} · гарсан ${fmtNumber(o.byDirection.outbound)}` : undefined} />
        <StatCard label="Хариулсан хувь" icon={<PhoneIncoming />} value={ol ? '…' : fmtPercent(o?.answerRate)} hint={o ? `${fmtNumber(o.answered)} хариулсан` : undefined} />
        <StatCard label="Дундаж хугацаа" icon={<Clock />} value={ol ? '…' : o ? fmtDuration(o.avgDurationSec) : '—'} />
        <StatCard label="Нийт минут" icon={<Hourglass />} value={ol ? '…' : fmtMinutes(o?.totalMinutes)} />
        <StatCard label="Нийт зардал" icon={<Coins />} value={ol ? '…' : fmtMnt(o?.costMnt)} />
        <StatCard label="Дуудлагын зардал" icon={<Receipt />} value={ol ? '…' : fmtMnt(o?.costPerCallMnt)} hint="Нэг дуудлагад" />
      </div>

      <TimeseriesChart points={series.data} bucket={bucket} loading={series.isLoading} />

      <div className="grid gap-6 lg:grid-cols-2">
        <SentimentDonut sentiment={o?.sentiment} loading={ol} />
        <OutcomesList outcomes={o?.outcomes} loading={ol} />
      </div>

      <Heatmap cells={heat.data} loading={heat.isLoading} />
      <ProfilesTable rows={profiles.data} loading={profiles.isLoading} />
      <CampaignsTable rows={campaigns.data} loading={campaigns.isLoading} />
    </div>
  )
}
