import { lazy, Suspense, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ArrowDownLeft, ArrowLeft, ArrowUpRight, Phone } from 'lucide-react'
import { api } from '@/lib/api'
import { fmtDateTime, fmtDuration, fmtPhone } from '@/lib/utils'
import type { Call, Contact } from '@/lib/types'
import {
  Badge, Card, CardBody, CardHeader, CallStatusBadge, EmptyState, PageHeader, SentimentBadge, Skeleton, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'

const CallDrawer = lazy(() => import('@/features/calls/CallDrawer').then((m) => ({ default: m.CallDrawer })))

export function ContactDetailPage() {
  const { id = '' } = useParams()
  const [callId, setCallId] = useState<string | null>(null)
  const detail = useQuery({
    queryKey: ['contacts', 'detail', id],
    queryFn: () => api.get<{ contact: Contact; calls: Call[] }>(`/contacts/${id}`),
    enabled: !!id,
  })

  const back = <Link to="/contacts" className="mb-4 inline-flex items-center gap-1.5 text-xs text-[var(--fg-muted)] hover:text-[var(--fg)]"><ArrowLeft className="h-3.5 w-3.5" />Харилцагчид</Link>

  if (detail.isLoading) return <div>{back}<Skeleton className="mb-4 h-40 w-full" /><Skeleton className="h-64 w-full" /></div>
  if (detail.isError || !detail.data) {
    return <div>{back}<Card><EmptyState title="Харилцагч олдсонгүй" description={(detail.error as Error | null)?.message} /></Card></div>
  }
  const { contact, calls } = detail.data

  return (
    <div>
      {back}
      <PageHeader title={contact.name || fmtPhone(contact.phone)} description={contact.name ? fmtPhone(contact.phone) : undefined} />
      <Card className="mb-6">
        <CardHeader title="Харилцагчийн мэдээлэл" />
        <CardBody>
          <dl className="grid grid-cols-1 gap-4 text-sm sm:grid-cols-2">
            <div><dt className="text-xs text-[var(--fg-muted)]">Утас</dt><dd className="mt-1 flex items-center gap-2 tabular-nums"><Phone className="h-3.5 w-3.5" />{fmtPhone(contact.phone)}</dd></div>
            <div><dt className="text-xs text-[var(--fg-muted)]">Нэр</dt><dd className="mt-1">{contact.name || '—'}</dd></div>
            <div><dt className="text-xs text-[var(--fg-muted)]">Шошго</dt>
              <dd className="mt-1 flex flex-wrap gap-1">{contact.tags?.length ? contact.tags.map((t) => <Badge key={t}>{t}</Badge>) : '—'}</dd></div>
            <div><dt className="text-xs text-[var(--fg-muted)]">Үүсгэсэн / Шинэчилсэн</dt><dd className="mt-1">{fmtDateTime(contact.createdAt)} / {fmtDateTime(contact.updatedAt)}</dd></div>
            {contact.meta && Object.entries(contact.meta).map(([k, v]) => (
              <div key={k}><dt className="text-xs text-[var(--fg-muted)]">{k}</dt><dd className="mt-1">{v}</dd></div>
            ))}
          </dl>
        </CardBody>
      </Card>

      <Card>
        <CardHeader title="Сүүлийн дуудлагууд" description="Сүүлийн 20 дуудлага" />
        {calls.length === 0 ? (
          <EmptyState title="Дуудлага байхгүй" />
        ) : (
          <Table>
            <THead><tr><TH>Эхэлсэн</TH><TH className="w-10" /><TH>Төлөв</TH><TH>Үргэлжлэх</TH><TH>Сэтгэгдэл</TH><TH>Хураангуй</TH></tr></THead>
            <TBody>
              {calls.map((c) => (
                <TR key={c.id} className="cursor-pointer" onClick={() => setCallId(c.id)} data-testid="contact-call-row">
                  <TD className="whitespace-nowrap tabular-nums">{fmtDateTime(c.startedAt)}</TD>
                  <TD>{c.direction === 'inbound' ? <ArrowDownLeft className="h-4 w-4 text-emerald-400" aria-label="Ирсэн" /> : <ArrowUpRight className="h-4 w-4 text-sky-400" aria-label="Гарсан" />}</TD>
                  <TD><CallStatusBadge status={c.status} /></TD>
                  <TD className="tabular-nums">{fmtDuration(c.durationSec)}</TD>
                  <TD><SentimentBadge sentiment={c.sentiment} /></TD>
                  <TD className="max-w-72 truncate text-[var(--fg-muted)]" title={c.summary}>{c.summary || '—'}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      {callId && (
        <Suspense fallback={null}>
          <CallDrawer callId={callId} onClose={() => setCallId(null)} />
        </Suspense>
      )}
    </div>
  )
}
