import { useMemo, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, ChevronLeft, ChevronRight, ExternalLink, FileDown, FlaskConical, Settings2 } from 'lucide-react'
import {
  Badge, Button, CampaignStatusBadge, Card, CardBody, CardHeader, EmptyState, PageHeader, Select, Skeleton, StatCard, Table, TBody, TD, TH, THead, TR,
  type BadgeTone,
} from '@/components/ui'
import type { Campaign, CampaignOutcome, CampaignStats, CampaignTargetStatus } from '@/lib/types'
import { cn, fmtAgo, fmtPhone } from '@/lib/utils'
import { CampaignActions, ProgressBar } from './components'
import { CampaignSettingsDialog, canEditSettings } from './CampaignSettingsDialog'
import { PAGE_SIZE, useCampaign, useCampaignLive, useCampaignStats, useExportCampaign } from './hooks'
import { outcomeTone, scheduleChip, type OutcomeTone } from './schedule'

const targetTone: Record<CampaignTargetStatus, BadgeTone> = { pending: 'neutral', calling: 'warning', done: 'success', failed: 'danger', skipped: 'info' }
export const targetLabel: Record<CampaignTargetStatus, string> = {
  pending: 'Хүлээгдэж буй', calling: 'Явж байгаа', done: 'Дууссан', failed: 'Амжилтгүй', skipped: 'Алгассан',
}

export function TargetStatusBadge({ status }: { status: CampaignTargetStatus }) {
  return <Badge tone={targetTone[status]} dot pulse={status === 'calling'}>{targetLabel[status]}</Badge>
}

const toneBadge: Record<OutcomeTone, BadgeTone> = { success: 'success', neutral: 'neutral', warning: 'warning' }
const toneBar: Record<OutcomeTone, string> = { success: 'bg-emerald-500', neutral: 'bg-[var(--neutral-dot)]', warning: 'bg-amber-500' }

/** Bar list of outcome counts. Labels/tones come from campaign.outcomes; unknown codes fall back to the stats label. */
export function OutcomesCard({ campaign, stats }: { campaign: Campaign; stats?: CampaignStats }) {
  const rows = useMemo(() => {
    const counts = new Map((stats?.byOutcome ?? []).map((o) => [o.code, o]))
    const defined = (campaign.outcomes ?? []).map((o) => ({ code: o.code, label: o.label, tone: outcomeTone(o), count: counts.get(o.code)?.count ?? 0 }))
    const extra = (stats?.byOutcome ?? [])
      .filter((o) => !(campaign.outcomes ?? []).some((d) => d.code === o.code))
      .map((o) => ({ code: o.code, label: o.label || o.code, tone: 'neutral' as OutcomeTone, count: o.count }))
    return [...defined, ...extra]
  }, [campaign.outcomes, stats])
  if (rows.length === 0) return null
  const max = Math.max(1, ...rows.map((r) => r.count))
  const total = rows.reduce((n, r) => n + r.count, 0)
  return (
    <Card className="mb-4" data-testid="outcomes-card">
      <CardHeader title="Үр дүн" description={`${total} дуудлага ангилагдсан`} />
      <CardBody>
        <ul className="space-y-2.5">
          {rows.map((r) => (
            <li key={r.code} data-testid="outcome-bar" className="grid grid-cols-[minmax(7rem,12rem)_1fr_3rem] items-center gap-3 text-xs">
              <span className="truncate text-[var(--fg)]" title={r.code}>{r.label}</span>
              <div className="h-2 overflow-hidden rounded-full bg-[var(--surface-3)]">
                <div className={cn('h-full rounded-full', toneBar[r.tone])} style={{ width: `${(r.count / max) * 100}%` }} />
              </div>
              <span className="text-right font-medium tabular-nums text-[var(--fg)]">{r.count}</span>
            </li>
          ))}
        </ul>
      </CardBody>
    </Card>
  )
}

function OutcomeCell({ code, outcomes }: { code?: string; outcomes: CampaignOutcome[] }) {
  if (!code) return <span className="text-[var(--fg-subtle)]">—</span>
  const def = outcomes.find((o) => o.code === code)
  return <Badge tone={def ? toneBadge[outcomeTone(def)] : 'neutral'}>{def?.label ?? code}</Badge>
}

export function CampaignDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [page, setPage] = useState(0)
  const [filter, setFilter] = useState<'' | CampaignTargetStatus>('')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const q = useCampaign(id, { limit: PAGE_SIZE, offset: page * PAGE_SIZE })
  const stats = useCampaignStats(q.data?.campaign)
  const exporter = useExportCampaign()
  useCampaignLive(id)

  if (q.isLoading) {
    return <div className="space-y-4"><Skeleton className="h-10 w-72" /><Skeleton className="h-24 w-full" /><Skeleton className="h-64 w-full" /></div>
  }
  if (q.isError || !q.data) {
    return (
      <EmptyState title="Кампанит ажил олдсонгүй" description={(q.error as Error | null)?.message}
        action={<Button variant="secondary" onClick={() => navigate('/campaigns')}>Жагсаалт руу буцах</Button>} />
    )
  }

  const { campaign, targets } = q.data
  const items = targets.items ?? []
  const byStatus = stats.data?.byStatus
  const callingLocal = items.filter((t) => t.status === 'calling').length
  const skipped = byStatus?.skipped ?? campaign.skipped ?? 0
  const calling = byStatus?.calling ?? callingLocal
  const pending = byStatus?.pending ?? Math.max(0, campaign.total - campaign.completed - campaign.failed - skipped - calling)
  const shown = filter ? items.filter((t) => t.status === filter) : items
  const pages = Math.max(1, Math.ceil(targets.total / PAGE_SIZE))
  const chip = scheduleChip(campaign.schedule)
  const outcomes = campaign.outcomes ?? []
  const dryDone = campaign.status === 'paused' && (campaign.dryRunDialed ?? 0) > 0

  return (
    <div>
      <Link to="/campaigns" className="mb-3 inline-flex items-center gap-1 text-xs text-[var(--fg-muted)] hover:text-[var(--fg)]">
        <ArrowLeft className="h-3.5 w-3.5" />Кампанит ажлууд
      </Link>
      <PageHeader
        title={<span className="flex flex-wrap items-center gap-3">{campaign.name}<CampaignStatusBadge status={campaign.status} />
          {(campaign.dryRunLimit ?? 0) > 0 && <Badge tone="warning"><FlaskConical className="h-3 w-3" />Туршилт: {campaign.dryRunLimit}</Badge>}
          {chip && <Badge tone="neutral">{chip}</Badge>}</span>}
        description={campaign.script ? <span className="line-clamp-2 whitespace-pre-line">{campaign.script}</span> : undefined}
        actions={
          <div className="flex flex-wrap items-center justify-end gap-1.5">
            <Button size="md" variant="outline" loading={exporter.isPending} onClick={() => exporter.mutate({ id: campaign.id, name: campaign.name })}>
              <FileDown className="h-3.5 w-3.5" />Excel татах
            </Button>
            {canEditSettings(campaign) && (
              <Button size="md" variant="outline" onClick={() => setSettingsOpen(true)}><Settings2 className="h-3.5 w-3.5" />Тохиргоо засах</Button>
            )}
            <CampaignActions campaign={campaign} size="md" detailed onDeleted={() => navigate('/campaigns')} />
          </div>} />

      {dryDone && (
        <div role="status" data-testid="dryrun-banner"
          className="mb-4 flex items-center gap-2 rounded-md border border-[var(--warning-border)] bg-[var(--warning-soft)] px-4 py-3 text-sm text-[var(--warning-fg)]">
          <FlaskConical className="h-4 w-4 shrink-0" />
          Туршилт дууслаа: {campaign.dryRunDialed} дуудлага. Сонсоод бүгдийг эхлүүлнэ үү
        </div>
      )}

      <div className="mb-4 grid grid-cols-2 gap-3 md:grid-cols-3 lg:grid-cols-6">
        <StatCard label="Нийт" value={campaign.total} />
        <StatCard label="Дууссан" value={byStatus?.done ?? campaign.completed} />
        <StatCard label="Амжилтгүй" value={byStatus?.failed ?? campaign.failed} />
        <StatCard label="Алгассан" value={skipped} hint="Хориглосон жагсаалт" />
        <StatCard label="Хүлээгдэж буй" value={pending} />
        <StatCard label="Явж байгаа" value={calling} hint={`Зэрэг: ${campaign.concurrency}`} />
      </div>
      <Card className="mb-4 px-5 py-4"><ProgressBar campaign={campaign} /></Card>

      <OutcomesCard campaign={campaign} stats={stats.data} />

      <Card>
        <CardHeader title="Дугаарууд" description={`${targets.total} дугаар`}
          actions={
            <Select value={filter} onChange={(e) => setFilter(e.target.value as '' | CampaignTargetStatus)} aria-label="Төлөвөөр шүүх" className="h-8 w-44 text-xs"
              options={(Object.keys(targetLabel) as CampaignTargetStatus[]).map((s) => ({ value: s, label: targetLabel[s] }))} placeholder="Бүх төлөв" />
          } />
        {shown.length === 0 ? (
          <EmptyState title="Дугаар алга" description={filter ? 'Энэ хуудсанд шүүлтэд тохирох дугаар алга.' : undefined} />
        ) : (
          <Table>
            <THead>
              <tr>
                <TH>Утас</TH><TH>Нэр</TH><TH>Төлөв</TH><TH>Үр дүн</TH><TH>Тайлбар</TH><TH className="text-right">Оролдлого</TH>
                <TH>Сүүлийн алдаа</TH><TH>Дуудлага</TH><TH>Дараагийн оролдлого</TH>
              </tr>
            </THead>
            <TBody>
              {shown.map((t) => (
                <TR key={t.id}>
                  <TD className="whitespace-nowrap tabular-nums">{fmtPhone(t.phone)}</TD>
                  <TD>{t.name || '—'}</TD>
                  <TD><TargetStatusBadge status={t.status} /></TD>
                  <TD><OutcomeCell code={t.outcome} outcomes={outcomes} /></TD>
                  <TD className="max-w-56 truncate text-xs text-[var(--fg-muted)]" title={t.outcomeNote}>{t.outcomeNote || '—'}</TD>
                  <TD className="text-right tabular-nums">{t.attempts}</TD>
                  <TD className="max-w-56 truncate text-xs text-red-300" title={t.lastError}>{t.lastError || '—'}</TD>
                  <TD>
                    {t.callId ? (
                      <Link to={`/calls/${t.callId}`} className="inline-flex items-center gap-1 text-xs text-[var(--accent)] hover:underline">
                        Харах<ExternalLink className="h-3 w-3" />
                      </Link>
                    ) : '—'}
                  </TD>
                  <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtAgo(t.nextTryAt)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
        {targets.total > PAGE_SIZE && (
          <div className="flex items-center justify-between border-t border-[var(--border)] px-4 py-2 text-xs text-[var(--fg-muted)]">
            <span>{page * PAGE_SIZE + 1}–{Math.min(targets.total, (page + 1) * PAGE_SIZE)} / {targets.total}</span>
            <div className="flex items-center gap-1">
              <Button size="sm" variant="ghost" disabled={page === 0} onClick={() => setPage((p) => p - 1)} aria-label="Өмнөх"><ChevronLeft className="h-4 w-4" /></Button>
              <span className="tabular-nums">{page + 1} / {pages}</span>
              <Button size="sm" variant="ghost" disabled={page + 1 >= pages} onClick={() => setPage((p) => p + 1)} aria-label="Дараах"><ChevronRight className="h-4 w-4" /></Button>
            </div>
          </div>
        )}
      </Card>
      <CampaignSettingsDialog campaign={campaign} open={settingsOpen} onClose={() => setSettingsOpen(false)} />
    </div>
  )
}
