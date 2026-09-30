import { useEffect, useState } from 'react'
import { Navigate } from 'react-router-dom'
import { ChevronLeft, ChevronRight, Search } from 'lucide-react'
import { Activity, Building2, Clock, CreditCard, PhoneCall } from 'lucide-react'
import {
  Badge, Button, Card, EmptyState, Input, PageHeader, Skeleton, StatCard, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import { useAuth } from '@/app/auth'
import { fmtMinutes, fmtMnt, fmtNumber } from '@/features/analytics/format'
import { OrgDrawer } from './OrgDrawer'
import { PAGE_SIZE, useAdminOrgs, useAdminStats, useDebounced } from './hooks'
import { ORG_STATUS, SUB_STATUS } from './labels'

function StatsCards() {
  const { data, isLoading } = useAdminStats()
  const v = (n?: number, f: (n: number | undefined) => string = fmtNumber) => (isLoading ? '…' : f(n))
  return (
    <div className="grid grid-cols-2 gap-3 lg:grid-cols-5" data-testid="admin-stats">
      <StatCard label="Байгууллага" icon={<Building2 />} value={v(data?.orgs)} />
      <StatCard label="Идэвхтэй захиалга" icon={<Activity />} value={v(data?.activeSubscriptions)} />
      <StatCard label="MRR" icon={<CreditCard />} value={v(data?.mrrMnt, fmtMnt)} hint="Сарын давтагдах орлого" />
      <StatCard label="Өнөөдрийн дуудлага" icon={<PhoneCall />} value={v(data?.callsToday)} />
      <StatCard label="Өнөөдрийн минут" icon={<Clock />} value={v(data?.minutesToday, fmtMinutes)} />
    </div>
  )
}

function AdminContent() {
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(0)
  const [selected, setSelected] = useState<{ id: string; name: string } | null>(null)
  const q = useDebounced(search.trim(), 300)
  useEffect(() => { setPage(0) }, [q])

  const orgs = useAdminOrgs(q, page)
  const rows = orgs.data?.items ?? []
  const total = orgs.data?.total
  const hasNext = total != null ? (page + 1) * PAGE_SIZE < total : rows.length === PAGE_SIZE

  return (
    <div className="space-y-6">
      <PageHeader title="Платформ админ" description="Бүх байгууллага, захиалга, нэхэмжлэхийн удирдлага" />
      <StatsCards />

      <Card className="overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--border-subtle)] px-4 py-3">
          <div className="relative w-full max-w-xs">
            <Search className="pointer-events-none absolute left-2.5 top-2.5 h-3.5 w-3.5 text-[var(--fg-subtle)]" aria-hidden />
            <Input className="pl-8" placeholder="Байгууллага хайх…" aria-label="Байгууллага хайх" value={search} onChange={(e) => setSearch(e.target.value)} />
          </div>
          <div className="flex items-center gap-2 text-xs text-[var(--fg-muted)]">
            {total != null && <span className="tabular">{fmtNumber(total)} байгууллага</span>}
            <Button size="icon" variant="ghost" aria-label="Өмнөх хуудас" disabled={page === 0} onClick={() => setPage((p) => Math.max(0, p - 1))}><ChevronLeft /></Button>
            <span className="tabular">{page + 1}</span>
            <Button size="icon" variant="ghost" aria-label="Дараах хуудас" disabled={!hasNext} onClick={() => setPage((p) => p + 1)}><ChevronRight /></Button>
          </div>
        </div>
        {orgs.isLoading ? (
          <div className="space-y-2 p-4">{Array.from({ length: 5 }, (_, i) => <Skeleton key={i} className="h-9" />)}</div>
        ) : orgs.isError ? (
          <EmptyState title="Жагсаалт ачаалж чадсангүй" description={orgs.error.message} />
        ) : rows.length === 0 ? (
          <EmptyState title="Байгууллага олдсонгүй" />
        ) : (
          <Table aria-label="Байгууллагууд">
            <THead><TR><TH>Байгууллага</TH><TH>Багц</TH><TH>Төлөв</TH><TH className="text-right">Хэрэглэгч</TH><TH className="text-right">Ашигласан минут</TH></TR></THead>
            <TBody>
              {rows.map((r) => {
                const st = ORG_STATUS[r.org.status]
                const sub = r.subscription ? SUB_STATUS[r.subscription.status] : null
                return (
                  <TR key={r.org.id} className="cursor-pointer" onClick={() => setSelected({ id: r.org.id, name: r.org.name })}>
                    <TD>
                      <button type="button" className="text-left font-medium hover:underline" onClick={(e) => { e.stopPropagation(); setSelected({ id: r.org.id, name: r.org.name }) }}>
                        {r.org.name}
                      </button>
                      <div className="text-[11px] text-[var(--fg-subtle)]">{r.org.slug}</div>
                    </TD>
                    <TD>
                      <span className="font-mono text-xs">{r.subscription?.planCode ?? r.org.planCode}</span>
                      {sub && <Badge className="ml-2" tone={sub.tone}>{sub.label}</Badge>}
                    </TD>
                    <TD><Badge tone={st.tone} dot>{st.label}</Badge></TD>
                    <TD className="tabular text-right">{fmtNumber(r.users)}</TD>
                    <TD className="tabular text-right">
                      {r.usage ? `${fmtMinutes(r.usage.minutes)} / ${fmtNumber(r.usage.includedMinutes)}` : '—'}
                    </TD>
                  </TR>
                )
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <OrgDrawer orgId={selected?.id ?? null} name={selected?.name} onClose={() => setSelected(null)} />
    </div>
  )
}

/** Platform staff only: everyone else is sent back to the dashboard. */
export default function AdminPage() {
  const user = useAuth((s) => s.user)
  if (!user?.isPlatformAdmin) return <Navigate to="/" replace />
  return <AdminContent />
}
