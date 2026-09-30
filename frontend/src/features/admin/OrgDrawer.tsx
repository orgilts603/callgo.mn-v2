import { useState, type FormEvent, type ReactNode } from 'react'
import { toast } from 'sonner'
import { ChevronDown, ChevronRight, PauseCircle, PlayCircle, XCircle } from 'lucide-react'
import {
  Badge, Button, Card, CardHeader, ConfirmDialog, Dialog, Drawer, EmptyState, Field, Input, Select, Skeleton, Table, TBody, TD, Textarea, TH, THead, TR,
} from '@/components/ui'
import { fmtDateTime } from '@/lib/utils'
import type { Invoice, Organization, OrgStatus, Plan, Subscription, SubscriptionStatus } from '@/lib/types'
import { fmtMinutes, fmtMnt, fmtNumber } from '@/features/analytics/format'
import { useAdminMutations, useAdminOrg, usePlans, type SubscriptionBody } from './hooks'
import { INVOICE_STATUS, ORG_STATUS, SUB_STATUS_OPTIONS } from './labels'

const errMsg = (err: unknown, fallback: string) => (err instanceof Error && err.message ? err.message : fallback)
const dateInput = (iso?: string | null) => (iso ? iso.slice(0, 10) : '')

/** Parses the advanced JSON textarea; blank means "leave custom limits alone". */
export function parseCustomLimits(text: string): { value?: Partial<Plan>; error?: string } {
  const t = text.trim()
  if (!t) return {}
  try {
    const v: unknown = JSON.parse(t)
    if (typeof v !== 'object' || v === null || Array.isArray(v)) return { error: 'JSON объект байх ёстой' }
    return { value: v as Partial<Plan> }
  } catch {
    return { error: 'JSON буруу байна' }
  }
}

export function buildSubscriptionBody(
  f: { planCode: string; status: SubscriptionStatus; periodEnd: string; customLimits: string },
  initialPeriodEnd: string,
): { body?: SubscriptionBody; error?: string } {
  const limits = parseCustomLimits(f.customLimits)
  if (limits.error) return { error: limits.error }
  const body: SubscriptionBody = { planCode: f.planCode, status: f.status }
  if (f.periodEnd && f.periodEnd !== initialPeriodEnd) body.currentPeriodEnd = new Date(`${f.periodEnd}T23:59:59`).toISOString()
  if (limits.value) body.customLimits = limits.value
  return { body }
}

function SubscriptionForm({ orgId, subscription }: { orgId: string; subscription?: Subscription | null }) {
  const plans = usePlans()
  const { saveSubscription } = useAdminMutations(orgId)
  const initialEnd = dateInput(subscription?.currentPeriodEnd)
  const [planCode, setPlanCode] = useState(subscription?.planCode ?? '')
  const [status, setStatus] = useState<SubscriptionStatus>(subscription?.status ?? 'active')
  const [periodEnd, setPeriodEnd] = useState(initialEnd)
  const [advanced, setAdvanced] = useState(!!subscription?.customLimits)
  const [limits, setLimits] = useState(subscription?.customLimits ? JSON.stringify(subscription.customLimits, null, 2) : '')
  const [limitsError, setLimitsError] = useState<string | undefined>()

  const options = (plans.data ?? []).map((p) => ({ value: p.code, label: p.name || p.code }))
  if (planCode && !options.some((o) => o.value === planCode)) options.unshift({ value: planCode, label: planCode })

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const { body, error } = buildSubscriptionBody({ planCode, status, periodEnd, customLimits: limits }, initialEnd)
    setLimitsError(error)
    if (!body) return
    saveSubscription.mutate(body, {
      onSuccess: () => toast.success('Багц хадгалагдлаа'),
      onError: (err) => toast.error(errMsg(err, 'Багц хадгалж чадсангүй')),
    })
  }

  return (
    <Card>
      <CardHeader title="Багц ба захиалга" />
      <form onSubmit={submit} className="space-y-3 px-4 py-4" aria-label="Захиалгын форм">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Багц">
            <Select aria-label="Багц" value={planCode} onChange={(e) => setPlanCode(e.target.value)} options={options} placeholder="Багц сонгох" />
          </Field>
          <Field label="Төлөв">
            <Select aria-label="Захиалгын төлөв" value={status} onChange={(e) => setStatus(e.target.value as SubscriptionStatus)} options={SUB_STATUS_OPTIONS} />
          </Field>
          <Field label="Хугацаа дуусах өдөр" className="sm:col-span-2">
            <Input type="date" aria-label="Хугацаа дуусах өдөр" value={periodEnd} onChange={(e) => setPeriodEnd(e.target.value)} />
          </Field>
        </div>
        <div>
          <button type="button" onClick={() => setAdvanced((v) => !v)} aria-expanded={advanced}
            className="flex items-center gap-1 text-xs font-medium text-[var(--fg-muted)] hover:text-[var(--fg)]">
            {advanced ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />} Нарийвчилсан (custom limits)
          </button>
          {advanced && (
            <Field label="Custom limits (JSON)" className="mt-2" error={limitsError} hint='ж: {"includedMinutes": 5000, "maxUsers": 20}'>
              <Textarea aria-label="Custom limits JSON" className="font-mono text-xs" rows={6} value={limits} spellCheck={false}
                onChange={(e) => setLimits(e.target.value)} />
            </Field>
          )}
        </div>
        <div className="flex justify-end">
          <Button type="submit" size="sm" loading={saveSubscription.isPending} disabled={!planCode}>Хадгалах</Button>
        </div>
      </form>
    </Card>
  )
}

const STATUS_ACTIONS: { status: OrgStatus; label: string; confirm: string; icon: typeof PauseCircle; from: OrgStatus[]; tone: 'danger' | 'primary' }[] = [
  { status: 'suspended', label: 'Түр хаах', confirm: 'Байгууллагыг түр хаах уу?', icon: PauseCircle, from: ['active'], tone: 'danger' },
  { status: 'active', label: 'Идэвхжүүлэх', confirm: 'Байгууллагыг дахин идэвхжүүлэх үү?', icon: PlayCircle, from: ['suspended', 'closed'], tone: 'primary' },
  { status: 'closed', label: 'Хаах', confirm: 'Байгууллагыг бүрмөсөн хаах уу?', icon: XCircle, from: ['active', 'suspended'], tone: 'danger' },
]

function StatusActions({ org }: { org: Organization }) {
  const { setStatus } = useAdminMutations(org.id)
  const [pending, setPending] = useState<(typeof STATUS_ACTIONS)[number] | null>(null)
  const st = ORG_STATUS[org.status]
  return (
    <Card>
      <CardHeader title="Байгууллагын төлөв" actions={<Badge tone={st.tone} dot>{st.label}</Badge>} />
      <div className="flex flex-wrap gap-2 px-4 py-4">
        {STATUS_ACTIONS.filter((a) => a.from.includes(org.status)).map((a) => (
          <Button key={a.status} size="sm" variant={a.tone === 'danger' ? 'outline' : 'secondary'} onClick={() => setPending(a)}>
            <a.icon /> {a.label}
          </Button>
        ))}
      </div>
      {pending && (
        <ConfirmDialog open onClose={() => setPending(null)} title={pending.confirm} description={org.name} tone={pending.tone} confirmLabel={pending.label}
          loading={setStatus.isPending}
          onConfirm={() => setStatus.mutateAsync(pending.status).then(
            () => toast.success('Төлөв шинэчлэгдлээ'),
            (err: unknown) => { toast.error(errMsg(err, 'Төлөв өөрчилж чадсангүй')); throw err },
          )}>
          <p className="text-sm text-[var(--fg-muted)]">
            {pending.status === 'active' ? 'Хэрэглэгчид системд дахин бүрэн эрхээр нэвтэрнэ.' : 'Энэ үйлдэл байгууллагын хэрэглэгчдэд шууд нөлөөлнө.'}
          </p>
        </ConfirmDialog>
      )}
    </Card>
  )
}

function MarkPaidDialog({ invoice, orgId, onClose }: { invoice: Invoice; orgId: string; onClose: () => void }) {
  const { markPaid } = useAdminMutations(orgId)
  const [note, setNote] = useState('')
  const submit = (e?: FormEvent) => {
    e?.preventDefault()
    markPaid.mutate({ invoiceId: invoice.id, note: note.trim() }, {
      onSuccess: () => { toast.success('Нэхэмжлэх төлсөн гэж тэмдэглэгдлээ'); onClose() },
      onError: (err) => toast.error(errMsg(err, 'Тэмдэглэж чадсангүй')),
    })
  }
  return (
    <Dialog open onClose={onClose} title="Төлсөн гэж тэмдэглэх" description={`${invoice.number} · ${fmtMnt(invoice.totalMnt)}`}
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={onClose}>Болих</Button>
          <Button size="sm" loading={markPaid.isPending} onClick={() => submit()}>Тэмдэглэх</Button>
        </>
      }>
      <form onSubmit={submit}>
        <Field label="Тэмдэглэл" hint="ж: Дансаар шилжүүлсэн, гүйлгээний утга ...">
          <Textarea aria-label="Тэмдэглэл" autoFocus rows={3} value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>
      </form>
    </Dialog>
  )
}

function InvoicesCard({ orgId, invoices }: { orgId: string; invoices: Invoice[] }) {
  const [target, setTarget] = useState<Invoice | null>(null)
  return (
    <Card className="overflow-hidden">
      <CardHeader title="Нэхэмжлэх" />
      {invoices.length === 0 ? <EmptyState title="Нэхэмжлэх алга" className="py-8" /> : (
        <Table aria-label="Нэхэмжлэхүүд">
          <THead><TR><TH>Дугаар</TH><TH>Хугацаа</TH><TH className="text-right">Дүн</TH><TH>Төлөв</TH><TH /></TR></THead>
          <TBody>
            {invoices.map((inv) => {
              const st = INVOICE_STATUS[inv.status]
              return (
                <TR key={inv.id}>
                  <TD className="font-mono text-xs">{inv.number}</TD>
                  <TD className="text-xs text-[var(--fg-muted)]">{fmtDateTime(inv.periodStart).slice(0, 10)} — {fmtDateTime(inv.periodEnd).slice(0, 10)}</TD>
                  <TD className="tabular text-right">{fmtMnt(inv.totalMnt)}</TD>
                  <TD><Badge tone={st.tone}>{st.label}</Badge></TD>
                  <TD className="text-right">
                    {inv.status === 'open' && <Button size="sm" variant="secondary" onClick={() => setTarget(inv)}>Төлсөн гэж тэмдэглэх</Button>}
                  </TD>
                </TR>
              )
            })}
          </TBody>
        </Table>
      )}
      {target && <MarkPaidDialog invoice={target} orgId={orgId} onClose={() => setTarget(null)} />}
    </Card>
  )
}

function OrgBody({ orgId }: { orgId: string }) {
  const { data, isLoading, error } = useAdminOrg(orgId)
  if (isLoading) return <div className="space-y-4 p-5"><Skeleton className="h-24" /><Skeleton className="h-56" /><Skeleton className="h-32" /></div>
  if (error || !data) return <EmptyState title="Байгууллага олдсонгүй" description={error?.message} />
  const { org, subscription, plan, usage, invoices } = data
  const users = Array.isArray(data.users) ? data.users.length : data.users
  return (
    <div className="space-y-4 p-5">
      <Card>
        <dl className="grid grid-cols-2 gap-3 px-4 py-3.5 sm:grid-cols-4">
          <Item label="Багц">{plan?.name || org.planCode}</Item>
          <Item label="Хэрэглэгч">{users == null ? '—' : fmtNumber(users)}</Item>
          <Item label="Ашигласан минут">{usage ? `${fmtMinutes(usage.minutes)} / ${fmtNumber(usage.includedMinutes)}` : '—'}</Item>
          <Item label="Зардал">{usage ? fmtMnt(usage.costMnt) : '—'}</Item>
        </dl>
      </Card>
      {/* keyed so the form re-initialises when the server data changes */}
      <SubscriptionForm key={`${subscription?.id ?? 'none'}-${subscription?.updatedAt ?? ''}`} orgId={org.id} subscription={subscription} />
      <StatusActions org={org} />
      <InvoicesCard orgId={org.id} invoices={invoices ?? []} />
    </div>
  )
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] uppercase tracking-wide text-[var(--fg-subtle)]">{label}</dt>
      <dd className="mt-0.5 truncate text-sm text-[var(--fg)]">{children}</dd>
    </div>
  )
}

export function OrgDrawer({ orgId, name, onClose }: { orgId: string | null; name?: string; onClose: () => void }) {
  return (
    <Drawer open={!!orgId} onClose={onClose} width="max-w-2xl" title={name ?? 'Байгууллага'}>
      {orgId ? <OrgBody key={orgId} orgId={orgId} /> : null}
    </Drawer>
  )
}
