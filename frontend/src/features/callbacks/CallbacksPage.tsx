import { useMemo, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { CalendarClock, ChevronLeft, ChevronRight, PhoneCall, Plus, X } from 'lucide-react'
import {
  Badge, Button, Card, Dialog, EmptyState, Field, Input, PageHeader, Select, Skeleton, Table, TBody, TD, Textarea, TH, THead, TR, type BadgeTone,
} from '@/components/ui'
import type { CallbackRequest, CallbackStatus } from '@/lib/types'
import { fmtAgo, fmtDateTime, fmtPhone } from '@/lib/utils'
import { ConfirmDialog, ErrorNote, errMsg } from '../settings/common'
import { useAgentProfiles, useSIPNumbers } from '../settings/hooks'
import { normalizeNumber, validateNumber } from '../settings/SIPNumbersTab'
import { CALLBACKS_PAGE_SIZE, useCallbacks, useCreateCallback, useUpdateCallback } from './hooks'

export const STATUS_LABEL: Record<CallbackStatus, string> = {
  pending: 'Хүлээгдэж байна', dialed: 'Залгаж байна', done: 'Дууссан', canceled: 'Цуцалсан', failed: 'Амжилтгүй',
}
const STATUS_TONE: Record<CallbackStatus, BadgeTone> = { pending: 'warning', dialed: 'info', done: 'success', canceled: 'neutral', failed: 'danger' }

export function CallbackStatusBadge({ status }: { status: CallbackStatus }) {
  return <Badge tone={STATUS_TONE[status]} dot pulse={status === 'dialed'}>{STATUS_LABEL[status]}</Badge>
}

/** `<input type="datetime-local">` value (local time) for an ISO instant. */
export function toLocalInput(iso: string | Date): string {
  const d = typeof iso === 'string' ? new Date(iso) : new Date(iso.getTime())
  d.setMinutes(d.getMinutes() - d.getTimezoneOffset())
  return d.toISOString().slice(0, 16)
}
/** Local datetime-local value → RFC 3339 instant; null when empty/invalid. */
export function fromLocalInput(v: string): string | null {
  if (!v) return null
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? null : d.toISOString()
}
const defaultDue = () => toLocalInput(new Date(Date.now() + 60 * 60_000))

// ---------------------------------------------------------------- create

export function CreateCallbackDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  if (!open) return null
  return <CreateForm onClose={onClose} />
}

function CreateForm({ onClose }: { onClose: () => void }) {
  const create = useCreateCallback()
  const sips = useSIPNumbers()
  const profiles = useAgentProfiles()
  const [phone, setPhone] = useState('+976')
  const [name, setName] = useState('')
  const [note, setNote] = useState('')
  const [dueAt, setDueAt] = useState(defaultDue)
  const [sipNumberId, setSipNumberId] = useState('')
  const [agentProfileId, setAgentProfileId] = useState('')
  const [touched, setTouched] = useState(false)

  const phoneError = touched ? validateNumber(phone) : null
  const dueError = touched && !fromLocalInput(dueAt) ? 'Цаг оруулна уу' : null

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    const due = fromLocalInput(dueAt)
    if (validateNumber(phone) || !due) return
    create.mutate({
      phone: normalizeNumber(phone), name: name.trim() || undefined, note: note.trim() || undefined, dueAt: due,
      sipNumberId: sipNumberId || undefined, agentProfileId: agentProfileId || undefined,
    }, {
      onSuccess: () => { toast.success('Буцаж залгах товлогдлоо'); onClose() },
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <Dialog open onClose={onClose} title="Буцаж залгах товлох" description="Товлосон цагт агент автоматаар залгана (SIP дугаарын ажлын цагийг хүндэтгэнэ).">
      <form onSubmit={submit} className="space-y-4" noValidate>
        <Field label="Утасны дугаар" hint="+976 болон 8 оронтой дугаар. 8 оронтой дугаарт +976 автоматаар нэмэгдэнэ." error={phoneError}>
          <Input value={phone} onChange={(e) => setPhone(e.target.value)} onBlur={() => setTouched(true)} inputMode="tel" placeholder="+97699112233" autoFocus />
        </Field>
        <Field label="Нэр"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
        <Field label="Тэмдэглэл" hint="Агентад дамжуулах дотоод тэмдэглэл"><Textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} /></Field>
        <Field label="Залгах цаг" error={dueError}><Input type="datetime-local" value={dueAt} onChange={(e) => setDueAt(e.target.value)} /></Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="SIP дугаар">
            <Select value={sipNumberId} onChange={(e) => setSipNumberId(e.target.value)} placeholder="— Автомат —" options={(sips.data ?? []).map((s) => ({ value: s.id, label: s.label || s.number }))} />
          </Field>
          <Field label="Агент профайл">
            <Select value={agentProfileId} onChange={(e) => setAgentProfileId(e.target.value)} placeholder="— Автомат —" options={(profiles.data ?? []).map((p) => ({ value: p.id, label: p.name }))} />
          </Field>
        </div>
        <ErrorNote error={create.error} />
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
          <Button type="submit" loading={create.isPending}>Товлох</Button>
        </div>
      </form>
    </Dialog>
  )
}

// ---------------------------------------------------------------- reschedule

function RescheduleDialog({ callback, onClose }: { callback: CallbackRequest | null; onClose: () => void }) {
  if (!callback) return null
  return <RescheduleForm key={callback.id} callback={callback} onClose={onClose} />
}

function RescheduleForm({ callback, onClose }: { callback: CallbackRequest; onClose: () => void }) {
  const update = useUpdateCallback()
  const [dueAt, setDueAt] = useState(() => toLocalInput(callback.dueAt))
  const due = fromLocalInput(dueAt)
  return (
    <Dialog open onClose={onClose} title="Цаг өөрчлөх" description={`${fmtPhone(callback.phone)}${callback.name ? ` · ${callback.name}` : ''}`}
      footer={<>
        <Button variant="ghost" onClick={onClose}>Болих</Button>
        <Button disabled={!due} loading={update.isPending} onClick={() => due && update.mutate({ id: callback.id, body: { dueAt: due } }, {
          onSuccess: () => { toast.success('Цаг шинэчлэгдлээ'); onClose() }, onError: (e) => toast.error(errMsg(e)),
        })}>Хадгалах</Button>
      </>}>
      <Field label="Шинэ цаг"><Input type="datetime-local" value={dueAt} onChange={(e) => setDueAt(e.target.value)} autoFocus /></Field>
    </Dialog>
  )
}

// ---------------------------------------------------------------- page

const STATUS_FILTERS: { value: CallbackStatus | ''; label: string }[] = [
  { value: '', label: 'Бүх төлөв' },
  ...(Object.keys(STATUS_LABEL) as CallbackStatus[]).map((s) => ({ value: s, label: STATUS_LABEL[s] })),
]

export function CallbacksPage({ embedded = false }: { embedded?: boolean } = {}) {
  const [status, setStatus] = useState<CallbackStatus | ''>('')
  const [offset, setOffset] = useState(0)
  const list = useCallbacks({ status, offset })
  const update = useUpdateCallback()
  const sips = useSIPNumbers()
  const profiles = useAgentProfiles()
  const [creating, setCreating] = useState(false)
  const [rescheduling, setRescheduling] = useState<CallbackRequest | null>(null)
  const [canceling, setCanceling] = useState<CallbackRequest | null>(null)

  const sipLabel = useMemo(() => new Map((sips.data ?? []).map((s) => [s.id, s.label || s.number])), [sips.data])
  const profileName = useMemo(() => new Map((profiles.data ?? []).map((p) => [p.id, p.name])), [profiles.data])
  const items = list.data?.items ?? []
  const total = list.data?.total ?? 0

  return (
    <div>
      {!embedded && (
        <PageHeader title="Буцаж залгах" description="Товлогдсон буцаж залгах хүсэлтүүд"
          actions={<Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Товлох</Button>} />
      )}
      <div className="mb-3 flex items-center justify-between gap-2">
        <Select aria-label="Төлөвөөр шүүх" className="w-48" value={status} onChange={(e) => { setStatus(e.target.value as CallbackStatus | ''); setOffset(0) }} options={STATUS_FILTERS} />
        {embedded && <Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Товлох</Button>}
      </div>
      <Card>
        {list.isLoading ? (
          <div className="space-y-2 p-4">{[0, 1, 2].map((i) => <Skeleton key={i} className="h-10 w-full" />)}</div>
        ) : list.isError ? (
          <EmptyState title="Ачаалж чадсангүй" description={errMsg(list.error)} action={<Button variant="secondary" onClick={() => void list.refetch()}>Дахин оролдох</Button>} />
        ) : items.length === 0 ? (
          <EmptyState icon={<PhoneCall className="h-8 w-8" />} title="Буцаж залгах хүсэлт алга" description="Агент эсвэл та гараар товлосон буцаж залгах дуудлагууд энд харагдана."
            action={<Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Товлох</Button>} />
        ) : (
          <>
            <Table>
              <THead><TR><TH>Дугаар</TH><TH>Нэр</TH><TH>Тэмдэглэл</TH><TH>Залгах цаг</TH><TH>Төлөв</TH><TH className="text-right">Оролдлого</TH><TH>Дуудлага</TH><TH className="text-right">Үйлдэл</TH></TR></THead>
              <TBody>
                {items.map((c) => {
                  const open = c.status === 'pending'
                  return (
                    <TR key={c.id}>
                      <TD className="whitespace-nowrap font-mono tabular-nums">{fmtPhone(c.phone)}</TD>
                      <TD>{c.name || <span className="text-[var(--fg-subtle)]">—</span>}</TD>
                      <TD className="max-w-xs truncate text-[var(--fg-muted)]" title={c.note}>{c.note || '—'}</TD>
                      <TD className="whitespace-nowrap">
                        <div>{fmtDateTime(c.dueAt)}</div>
                        <div className="text-xs text-[var(--fg-subtle)]">{fmtAgo(c.dueAt)}</div>
                      </TD>
                      <TD>
                        <CallbackStatusBadge status={c.status} />
                        {(c.sipNumberId || c.agentProfileId) && (
                          <div className="mt-0.5 text-[11px] text-[var(--fg-subtle)]">
                            {[c.sipNumberId && sipLabel.get(c.sipNumberId), c.agentProfileId && profileName.get(c.agentProfileId)].filter(Boolean).join(' · ')}
                          </div>
                        )}
                      </TD>
                      <TD className="text-right tabular-nums">{c.attempts}</TD>
                      <TD>{c.resultCallId ? <Link to={`/calls/${c.resultCallId}`} className="text-[var(--accent)] hover:underline">Харах</Link> : <span className="text-[var(--fg-subtle)]">—</span>}</TD>
                      <TD className="text-right">
                        {open && (
                          <div className="flex justify-end gap-1">
                            <Button size="sm" variant="outline" aria-label={`Цаг өөрчлөх ${c.phone}`} onClick={() => setRescheduling(c)}><CalendarClock className="h-3.5 w-3.5" />Цаг өөрчлөх</Button>
                            <Button size="sm" variant="ghost" aria-label={`Цуцлах ${c.phone}`} onClick={() => setCanceling(c)}><X className="h-3.5 w-3.5 text-red-400" />Цуцлах</Button>
                          </div>
                        )}
                      </TD>
                    </TR>
                  )
                })}
              </TBody>
            </Table>
            <div className="flex items-center justify-between border-t border-[var(--border-subtle)] px-3 py-2 text-xs text-[var(--fg-muted)]">
              <span className="tabular-nums">{offset + 1}–{offset + items.length} / {total}</span>
              <div className="flex gap-1">
                <Button size="icon" variant="ghost" aria-label="Өмнөх" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - CALLBACKS_PAGE_SIZE))}><ChevronLeft className="h-4 w-4" /></Button>
                <Button size="icon" variant="ghost" aria-label="Дараах" disabled={offset + items.length >= total} onClick={() => setOffset(offset + CALLBACKS_PAGE_SIZE)}><ChevronRight className="h-4 w-4" /></Button>
              </div>
            </div>
          </>
        )}
      </Card>

      <CreateCallbackDialog open={creating} onClose={() => setCreating(false)} />
      <RescheduleDialog callback={rescheduling} onClose={() => setRescheduling(null)} />
      <ConfirmDialog open={canceling !== null} title="Хүсэлтийг цуцлах уу?" description={canceling ? `${fmtPhone(canceling.phone)} — ${fmtDateTime(canceling.dueAt)}` : undefined}
        confirmLabel="Цуцлах" loading={update.isPending} onClose={() => setCanceling(null)}
        onConfirm={() => canceling && update.mutate({ id: canceling.id, body: { status: 'canceled' } }, {
          onSuccess: () => { toast.success('Цуцлагдлаа'); setCanceling(null) }, onError: (e) => toast.error(errMsg(e)),
        })} />
    </div>
  )
}
