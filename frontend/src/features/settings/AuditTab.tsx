import { Fragment, useEffect, useState } from 'react'
import { ChevronDown, ChevronLeft, ChevronRight, ScrollText, X } from 'lucide-react'
import { Button, Card, CardHeader, EmptyState, Input, Select, Skeleton, Table, TBody, TD, TH, THead, TR } from '@/components/ui'
import { HttpError } from '@/lib/api'
import { cn, fmtDateTime } from '@/lib/utils'
import { ErrorNote } from './common'
import { AUDIT_PAGE_SIZE, dayEndISO, dayStartISO } from './identity'
import { useAuditLog, useMembers, type AuditFilters } from './hooks'

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => { const t = setTimeout(() => setV(value), ms); return () => clearTimeout(t) }, [value, ms])
  return v
}

function hasMeta(meta?: Record<string, unknown>): meta is Record<string, unknown> {
  return !!meta && Object.keys(meta).length > 0
}

export default function AuditTab() {
  const [actorId, setActorId] = useState('')
  const [actionInput, setActionInput] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [page, setPage] = useState(0)
  const [open, setOpen] = useState<string | null>(null)
  const action = useDebounced(actionInput.trim(), 300)

  const filters: AuditFilters = {
    actorId: actorId || undefined, action: action || undefined, from: dayStartISO(from), to: dayEndISO(to),
    limit: AUDIT_PAGE_SIZE, offset: page * AUDIT_PAGE_SIZE,
  }
  const q = useAuditLog(filters)
  const members = useMembers()
  const actors = [{ value: '', label: 'Бүх хэрэглэгч' }, ...(members.data?.items ?? []).map((u) => ({ value: u.id, label: u.name ? `${u.name} (${u.email})` : u.email }))]

  const items = q.data?.items ?? []
  const total = q.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / AUDIT_PAGE_SIZE))
  const filtered = !!(actorId || action || from || to)
  const reset = <T,>(set: (v: T) => void) => (v: T) => { set(v); setPage(0) }

  if (q.error instanceof HttpError && q.error.status === 403) {
    return <Card><EmptyState icon={<ScrollText />} title="Хандах эрхгүй" description="Аудитын бүртгэлийг зөвхөн эзэмшигч болон админ харна." /></Card>
  }

  return (
    <Card>
      <CardHeader title="Аудитын бүртгэл" description="Байгууллагын хэмжээнд хийгдсэн бүх өөрчлөлт: хэн, хэзээ, юу хийсэн." />
      <div className="flex flex-wrap items-end gap-2 border-b border-[var(--border-subtle)] px-4 py-3">
        <label className="space-y-1">
          <span className="block text-[11px] font-medium text-[var(--fg-subtle)]">Хэрэглэгч</span>
          <Select aria-label="Хэрэглэгч" className="w-56" value={actorId} options={actors} onChange={(e) => reset(setActorId)(e.target.value)} />
        </label>
        <label className="space-y-1">
          <span className="block text-[11px] font-medium text-[var(--fg-subtle)]">Үйлдэл</span>
          <Input aria-label="Үйлдэл" className="w-48 font-mono text-xs" placeholder="user.invite" value={actionInput}
            onChange={(e) => reset(setActionInput)(e.target.value)} />
        </label>
        <label className="space-y-1">
          <span className="block text-[11px] font-medium text-[var(--fg-subtle)]">Эхлэх огноо</span>
          <Input aria-label="Эхлэх огноо" type="date" className="w-40" value={from} max={to || undefined} onChange={(e) => reset(setFrom)(e.target.value)} />
        </label>
        <label className="space-y-1">
          <span className="block text-[11px] font-medium text-[var(--fg-subtle)]">Дуусах огноо</span>
          <Input aria-label="Дуусах огноо" type="date" className="w-40" value={to} min={from || undefined} onChange={(e) => reset(setTo)(e.target.value)} />
        </label>
        {filtered && (
          <Button variant="ghost" size="sm" className="mb-0.5" onClick={() => { setActorId(''); setActionInput(''); setFrom(''); setTo(''); setPage(0) }}>
            <X /> Цэвэрлэх
          </Button>
        )}
      </div>

      {q.isLoading ? (
        <div className="space-y-2 p-4"><Skeleton className="h-8" /><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
      ) : q.isError ? (
        <div className="p-4"><ErrorNote error={q.error} /></div>
      ) : items.length === 0 ? (
        <EmptyState icon={<ScrollText />} title={filtered ? 'Шүүлтүүрт тохирох бичлэг олдсонгүй' : 'Аудитын бичлэг алга'} />
      ) : (
        <Table>
          <THead><TR><TH className="w-8"><span className="sr-only">Дэлгэрэнгүй</span></TH><TH>Огноо</TH><TH>Хэрэглэгч</TH><TH>Үйлдэл</TH><TH>Объект</TH><TH>IP</TH></TR></THead>
          <TBody>
            {items.map((e) => {
              const expandable = hasMeta(e.meta)
              const isOpen = open === e.id
              return (
                <Fragment key={e.id}>
                  <TR data-testid={`audit-${e.id}`}>
                    <TD className="pr-0">
                      {expandable && (
                        <button type="button" onClick={() => setOpen(isOpen ? null : e.id)} aria-expanded={isOpen}
                          aria-label={isOpen ? 'Дэлгэрэнгүйг хураах' : 'Дэлгэрэнгүй харах'}
                          className="flex h-6 w-6 items-center justify-center rounded text-[var(--fg-subtle)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)]">
                          <ChevronDown className={cn('h-3.5 w-3.5 transition-transform', !isOpen && '-rotate-90')} />
                        </button>
                      )}
                    </TD>
                    <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtDateTime(e.at)}</TD>
                    <TD className="max-w-56 truncate">{e.actorEmail || <span className="text-[var(--fg-subtle)]">Систем</span>}</TD>
                    <TD><code className="rounded bg-[var(--surface-2)] px-1.5 py-0.5 font-mono text-[11px] text-[var(--fg)]">{e.action}</code></TD>
                    <TD className="max-w-64 truncate text-xs text-[var(--fg-muted)]" title={`${e.targetType} ${e.targetId}`}>
                      {e.targetType ? <>{e.targetType}<span className="font-mono text-[var(--fg-subtle)]"> {e.targetId}</span></> : '—'}
                    </TD>
                    <TD className="font-mono text-xs text-[var(--fg-muted)]">{e.ip || '—'}</TD>
                  </TR>
                  {expandable && isOpen && (
                    <tr>
                      <td colSpan={6} className="bg-[var(--surface-inset)] px-4 py-3">
                        <pre data-testid="audit-meta" className="max-h-72 overflow-auto whitespace-pre-wrap break-all font-mono text-[11px] leading-relaxed text-[var(--fg-muted)]">
                          {JSON.stringify(e.meta, null, 2)}
                        </pre>
                      </td>
                    </tr>
                  )}
                </Fragment>
              )
            })}
          </TBody>
        </Table>
      )}
      {total > AUDIT_PAGE_SIZE && (
        <div className="flex items-center justify-between border-t border-[var(--border-subtle)] px-4 py-2 text-xs text-[var(--fg-muted)]">
          <span className="tabular-nums">{page * AUDIT_PAGE_SIZE + 1}–{Math.min(total, (page + 1) * AUDIT_PAGE_SIZE)} / {total}</span>
          <div className="flex items-center gap-1">
            <Button size="sm" variant="ghost" disabled={page === 0} onClick={() => setPage((p) => p - 1)} aria-label="Өмнөх"><ChevronLeft /></Button>
            <span className="tabular-nums">{page + 1} / {pages}</span>
            <Button size="sm" variant="ghost" disabled={page + 1 >= pages} onClick={() => setPage((p) => p + 1)} aria-label="Дараах"><ChevronRight /></Button>
          </div>
        </div>
      )}
    </Card>
  )
}
