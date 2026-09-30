import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft, ChevronLeft, ChevronRight, ExternalLink } from 'lucide-react'
import {
  Badge, Button, CampaignStatusBadge, Card, CardHeader, EmptyState, PageHeader, Select, Skeleton, StatCard, Table, TBody, TD, TH, THead, TR,
  type BadgeTone,
} from '@/components/ui'
import type { CampaignTargetStatus } from '@/lib/types'
import { fmtAgo, fmtPhone } from '@/lib/utils'
import { CampaignActions, ProgressBar } from './components'
import { PAGE_SIZE, useCampaign, useCampaignLive } from './hooks'

const targetTone: Record<CampaignTargetStatus, BadgeTone> = { pending: 'neutral', calling: 'warning', done: 'success', failed: 'danger' }
export const targetLabel: Record<CampaignTargetStatus, string> = { pending: 'Хүлээгдэж буй', calling: 'Явж байгаа', done: 'Дууссан', failed: 'Амжилтгүй' }

export function TargetStatusBadge({ status }: { status: CampaignTargetStatus }) {
  return <Badge tone={targetTone[status]} dot pulse={status === 'calling'}>{targetLabel[status]}</Badge>
}

export function CampaignDetailPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [page, setPage] = useState(0)
  const [filter, setFilter] = useState<'' | CampaignTargetStatus>('')
  const q = useCampaign(id, { limit: PAGE_SIZE, offset: page * PAGE_SIZE })
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
  const calling = items.filter((t) => t.status === 'calling').length
  const pending = Math.max(0, campaign.total - campaign.completed - campaign.failed - calling)
  const shown = filter ? items.filter((t) => t.status === filter) : items
  const pages = Math.max(1, Math.ceil(targets.total / PAGE_SIZE))

  return (
    <div>
      <Link to="/campaigns" className="mb-3 inline-flex items-center gap-1 text-xs text-[var(--fg-muted)] hover:text-[var(--fg)]">
        <ArrowLeft className="h-3.5 w-3.5" />Кампанит ажлууд
      </Link>
      <PageHeader
        title={<span className="flex items-center gap-3">{campaign.name}<CampaignStatusBadge status={campaign.status} /></span>}
        description={campaign.script ? <span className="line-clamp-2 whitespace-pre-line">{campaign.script}</span> : undefined}
        actions={<CampaignActions campaign={campaign} size="md" onDeleted={() => navigate('/campaigns')} />} />

      <div className="mb-4 grid grid-cols-2 gap-3 md:grid-cols-5">
        <StatCard label="Нийт" value={campaign.total} />
        <StatCard label="Дууссан" value={campaign.completed} />
        <StatCard label="Амжилтгүй" value={campaign.failed} />
        <StatCard label="Хүлээгдэж буй" value={pending} />
        <StatCard label="Явж байгаа" value={calling} hint={`Зэрэг: ${campaign.concurrency}`} />
      </div>
      <Card className="mb-4 px-5 py-4"><ProgressBar campaign={campaign} /></Card>

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
              <tr><TH>Утас</TH><TH>Нэр</TH><TH>Төлөв</TH><TH className="text-right">Оролдлого</TH><TH>Сүүлийн алдаа</TH><TH>Дуудлага</TH><TH>Дараагийн оролдлого</TH></tr>
            </THead>
            <TBody>
              {shown.map((t) => (
                <TR key={t.id}>
                  <TD className="whitespace-nowrap tabular-nums">{fmtPhone(t.phone)}</TD>
                  <TD>{t.name || '—'}</TD>
                  <TD><TargetStatusBadge status={t.status} /></TD>
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
    </div>
  )
}
