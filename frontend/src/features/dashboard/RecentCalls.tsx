import { Link, useNavigate } from 'react-router-dom'
import { ArrowRight, PhoneIncoming, PhoneOff, PhoneOutgoing } from 'lucide-react'
import { Card, CardHeader, CallStatusBadge, EmptyState, SentimentBadge, Skeleton, Table, TBody, TD, TH, THead, TR } from '@/components/ui'
import { fmtDuration, fmtPhone } from '@/lib/utils'
import type { Call } from '@/lib/types'
import { fmtAgoMn } from './format'

function counterpart(c: Call): string {
  return c.direction === 'inbound' ? c.fromNumber : c.toNumber
}

export function RecentCalls({ calls, loading, error, onRetry }: { calls?: Call[]; loading: boolean; error?: boolean; onRetry?: () => void }) {
  const navigate = useNavigate()
  return (
    <Card>
      <CardHeader
        title="Сүүлийн дуудлагууд"
        description="Хамгийн сүүлийн 10 дуудлага"
        actions={
          <Link to="/calls" className="inline-flex items-center gap-1 text-xs font-medium text-[var(--fg-muted)] transition-colors hover:text-[var(--fg)]">
            Бүгдийг харах <ArrowRight className="h-3.5 w-3.5" />
          </Link>
        }
      />
      {loading ? (
        <div className="space-y-2.5 p-4">
          {Array.from({ length: 5 }, (_, i) => <Skeleton key={i} className="h-7 w-full" />)}
        </div>
      ) : error ? (
        <EmptyState icon={<PhoneOff />} title="Ачаалж чадсангүй" description="Дуудлагын жагсаалтыг татах үед алдаа гарлаа."
          action={onRetry && <button type="button" onClick={onRetry} className="text-xs font-medium text-[var(--accent-fg)] hover:underline">Дахин оролдох</button>} />
      ) : !calls || calls.length === 0 ? (
        <EmptyState icon={<PhoneIncoming />} title="Дуудлага алга" description="Шинэ дуудлага ирэх эсвэл кампанит ажил эхлэхэд энд харагдана." />
      ) : (
        <Table>
          <THead>
            <tr>
              <TH className="pl-4">Дугаар</TH>
              <TH>Төлөв</TH>
              <TH>Сэтгэл хандлага</TH>
              <TH className="text-right">Хугацаа</TH>
              <TH className="pr-4 text-right">Огноо</TH>
            </tr>
          </THead>
          <TBody>
            {calls.map((c) => {
              const Icon = c.direction === 'inbound' ? PhoneIncoming : PhoneOutgoing
              return (
                <TR key={c.id} className="cursor-pointer" onClick={() => navigate(`/calls/${c.id}`)}>
                  <TD className="pl-4">
                    <Link to={`/calls/${c.id}`} onClick={(e) => e.stopPropagation()} className="inline-flex items-center gap-2.5 font-medium hover:text-[var(--accent-fg)]">
                      <span className="flex h-6 w-6 items-center justify-center rounded-md bg-[var(--surface-2)] text-[var(--fg-muted)]"
                        title={c.direction === 'inbound' ? 'Ирсэн дуудлага' : 'Гарсан дуудлага'}>
                        <Icon className="h-3.5 w-3.5" />
                      </span>
                      <span className="tabular">{fmtPhone(counterpart(c))}</span>
                    </Link>
                  </TD>
                  <TD><CallStatusBadge status={c.status} /></TD>
                  <TD><SentimentBadge sentiment={c.sentiment} /></TD>
                  <TD className="tabular text-right text-[var(--fg-muted)]">{fmtDuration(c.durationSec)}</TD>
                  <TD className="pr-4 text-right text-xs text-[var(--fg-muted)]" title={c.startedAt}>{fmtAgoMn(c.startedAt)}</TD>
                </TR>
              )
            })}
          </TBody>
        </Table>
      )}
    </Card>
  )
}
