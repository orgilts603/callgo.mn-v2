import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { ExternalLink, Megaphone, Plus } from 'lucide-react'
import {
  Button, CampaignStatusBadge, Card, EmptyState, PageHeader, Skeleton, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import { fmtAgo } from '@/lib/utils'
import { CampaignActions, ProgressBar } from './components'
import { useAgentProfiles, useCampaignLive, useCampaigns, useSipNumbers } from './hooks'
import { NewCampaignDialog } from './NewCampaignDialog'

export function CampaignsPage() {
  const campaigns = useCampaigns()
  const sips = useSipNumbers()
  const profiles = useAgentProfiles()
  const [open, setOpen] = useState(false)
  useCampaignLive()

  const sipLabel = useMemo(() => new Map((sips.data ?? []).map((s) => [s.id, s.label || s.number])), [sips.data])
  const profileName = useMemo(() => new Map((profiles.data ?? []).map((p) => [p.id, p.name])), [profiles.data])
  const items = campaigns.data ?? []

  return (
    <div>
      <PageHeader title="Кампанит ажил" description="Автомат гарах дуудлагын кампанит ажлууд"
        actions={<Button onClick={() => setOpen(true)}><Plus className="h-4 w-4" />Шинэ кампанит ажил</Button>} />
      <Card>
        {campaigns.isLoading ? (
          <div className="space-y-2 p-4">{[0, 1, 2].map((i) => <Skeleton key={i} className="h-10 w-full" />)}</div>
        ) : campaigns.isError ? (
          <EmptyState title="Ачаалж чадсангүй" description={(campaigns.error as Error).message}
            action={<Button variant="secondary" onClick={() => void campaigns.refetch()}>Дахин оролдох</Button>} />
        ) : items.length === 0 ? (
          <EmptyState icon={<Megaphone className="h-8 w-8" />} title="Кампанит ажил алга"
            description="CSV жагсаалтаар гарах дуудлагын кампанит ажил үүсгээрэй."
            action={<Button onClick={() => setOpen(true)}><Plus className="h-4 w-4" />Шинэ кампанит ажил</Button>} />
        ) : (
          <Table>
            <THead>
              <tr>
                <TH>Нэр</TH><TH>Төлөв</TH><TH>SIP дугаар</TH><TH>Агент</TH><TH>Явц</TH>
                <TH className="text-right">Зэрэгцээ</TH><TH>Үүсгэсэн</TH><TH className="text-right">Үйлдэл</TH>
              </tr>
            </THead>
            <TBody>
              {items.map((c) => (
                <TR key={c.id}>
                  <TD><Link to={`/campaigns/${c.id}`} className="font-medium hover:text-[var(--accent)]">{c.name}</Link></TD>
                  <TD><CampaignStatusBadge status={c.status} /></TD>
                  <TD className="text-[var(--fg-muted)]">{(c.sipNumberId && sipLabel.get(c.sipNumberId)) || '—'}</TD>
                  <TD className="text-[var(--fg-muted)]">{(c.agentProfileId && profileName.get(c.agentProfileId)) || '—'}</TD>
                  <TD><ProgressBar campaign={c} /></TD>
                  <TD className="text-right tabular-nums">{c.concurrency}</TD>
                  <TD className="whitespace-nowrap text-[var(--fg-muted)]">{fmtAgo(c.createdAt)}</TD>
                  <TD>
                    <div className="flex items-center justify-end gap-1.5">
                      <CampaignActions campaign={c} />
                      <Link to={`/campaigns/${c.id}`} aria-label="Дэлгэрэнгүй" title="Дэлгэрэнгүй"
                        className="inline-flex h-8 w-8 items-center justify-center rounded-md text-[var(--fg-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)]">
                        <ExternalLink className="h-3.5 w-3.5" />
                      </Link>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
      <NewCampaignDialog open={open} onClose={() => setOpen(false)} />
    </div>
  )
}
