import { useState } from 'react'
import { toast } from 'sonner'
import { AlertTriangle, Check, Clock, Mail } from 'lucide-react'
import { Badge, Button, Card, ConfirmDialog, EmptyState, Skeleton } from '@/components/ui'
import { cn } from '@/lib/utils'
import type { Invoice, Plan } from '@/lib/types'
import { useCancelPlan, useChangePlan, useInvoices, usePlans, useSubscription, type SubscriptionResponse } from './hooks'
import {
  daysUntil, fmtDate, fmtMnt, fmtPeriod, isCustomPlan, planBullets, SALES_MAILTO, subStatusLabel, subStatusTone, TRIAL_CODE,
} from './format'
import { errMsg } from './errors'
import { PayDialog } from './PayDialog'

export function TrialChip({ trialEndsAt, now }: { trialEndsAt?: string | null; now?: number }) {
  const days = daysUntil(trialEndsAt, now)
  if (days === null) return null
  const tone = days <= 3 ? 'warning' : 'info'
  return (
    <Badge tone={tone} data-testid="trial-chip">
      <Clock className="h-3 w-3" aria-hidden />
      {days > 0 ? `Туршилт дуусахад ${days} хоног` : 'Туршилтын хугацаа дууссан'}
    </Badge>
  )
}

function SubscriptionSummary({ data, onPay, openInvoice }: { data: SubscriptionResponse; onPay: (inv: Invoice) => void; openInvoice?: Invoice }) {
  const { subscription: sub, plan } = data
  const cancel = useCancelPlan()
  const [confirmCancel, setConfirmCancel] = useState(false)
  const canCancel = sub.status !== 'canceled' && !sub.canceledAt && plan.code !== TRIAL_CODE
  return (
    <Card className="mb-4 px-4 py-3.5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-[13px] text-[var(--fg-muted)]">Одоогийн багц:</span>
            <span className="text-sm font-semibold text-[var(--fg)]">{plan.name}</span>
            <Badge tone={subStatusTone[sub.status]} dot>{subStatusLabel[sub.status]}</Badge>
            {sub.status === 'trialing' && <TrialChip trialEndsAt={sub.trialEndsAt} />}
          </div>
          <div className="mt-1 text-xs text-[var(--fg-muted)]">
            Тооцооны үе: {fmtPeriod(sub.currentPeriodStart, sub.currentPeriodEnd)}
            {sub.canceledAt && sub.status !== 'canceled' && <> · {fmtDate(sub.currentPeriodEnd)}-нд цуцлагдана</>}
          </div>
        </div>
        {canCancel && <Button variant="ghost" size="sm" onClick={() => setConfirmCancel(true)}>Багц цуцлах</Button>}
      </div>
      {sub.status === 'past_due' && (
        <div role="alert" className="mt-3 flex flex-wrap items-center justify-between gap-3 rounded-[var(--radius)] border border-[var(--danger-border)] bg-[var(--danger-soft)] px-3 py-2 text-xs text-[var(--danger-fg)]">
          <span className="flex items-center gap-2"><AlertTriangle className="h-4 w-4 shrink-0" aria-hidden />Төлбөрийн хугацаа хэтэрсэн байна. Үйлчилгээ зогсохоос өмнө нэхэмжлэхээ төлнө үү.</span>
          {openInvoice && <Button size="sm" variant="danger" onClick={() => onPay(openInvoice)}>Төлөх · {fmtMnt(openInvoice.totalMnt)}</Button>}
        </div>
      )}
      <ConfirmDialog open={confirmCancel} onClose={() => setConfirmCancel(false)} title="Багц цуцлах уу?"
        description={`Багц одоогийн тооцооны үеийн төгсгөлд (${fmtDate(sub.currentPeriodEnd)}) цуцлагдана.`}
        confirmLabel="Цуцлах"
        onConfirm={async () => {
          try { await cancel.mutateAsync(); toast.success('Багц хугацааны төгсгөлд цуцлагдана') } catch (e) { toast.error(errMsg(e)); throw e }
        }} />
    </Card>
  )
}

function priceLabel(p: Plan): { main: string; sub?: string } {
  if (isCustomPlan(p)) return { main: 'Тохиролцоно' }
  if (p.monthlyMnt <= 0) return { main: 'Үнэгүй', sub: p.trialDays > 0 ? `${p.trialDays} хоног` : undefined }
  return { main: fmtMnt(p.monthlyMnt), sub: '/ сар, НӨАТ ороогүй' }
}

export function PlansGrid() {
  const plans = usePlans()
  const sub = useSubscription()
  const invoices = useInvoices()
  const change = useChangePlan()
  const [selected, setSelected] = useState<Plan | null>(null)
  const [payInvoice, setPayInvoice] = useState<Invoice | null>(null)
  const currentCode = sub.data?.subscription.planCode
  const openInvoice = invoices.data?.find((i) => i.status === 'open')

  const confirmChange = async () => {
    if (!selected) return
    try {
      const res = await change.mutateAsync(selected.code)
      if (res.invoice && res.invoice.status === 'open') {
        toast.success('Нэхэмжлэх үүслээ. Төлбөр төлөгдсөний дараа багц идэвхжинэ.')
        setPayInvoice(res.invoice)
      } else {
        toast.success(`"${selected.name}" багц идэвхжлээ`)
      }
    } catch (e) {
      toast.error(errMsg(e))
      throw e
    }
  }

  return (
    <div>
      {sub.data && <SubscriptionSummary data={sub.data} onPay={setPayInvoice} openInvoice={openInvoice} />}
      {plans.isLoading ? (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">{[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-80" />)}</div>
      ) : plans.isError ? (
        <Card><EmptyState icon={<AlertTriangle />} title="Багцын мэдээлэл ачаалж чадсангүй" description={errMsg(plans.error)} /></Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
          {(plans.data ?? []).map((p) => {
            const current = p.code === currentCode
            const price = priceLabel(p)
            return (
              <Card key={p.code} data-testid={`plan-${p.code}`} aria-current={current || undefined}
                className={cn('flex flex-col px-4 py-4', current && 'border-[var(--accent)] border-t-[var(--accent)] ring-1 ring-[var(--accent)]')}>
                <div className="flex items-center justify-between gap-2">
                  <h3 className="text-sm font-semibold text-[var(--fg)]">{p.name}</h3>
                  {current && <Badge tone="accent">Одоогийн багц</Badge>}
                </div>
                <div className="mt-3">
                  <span className="font-mono text-xl font-medium tabular-nums text-[var(--fg)]">{price.main}</span>
                  {price.sub && <span className="ml-1 text-xs text-[var(--fg-muted)]">{price.sub}</span>}
                </div>
                <ul className="mt-4 flex-1 space-y-1.5 text-[13px]">
                  {planBullets(p).map((b) => (
                    <li key={b} className="flex items-start gap-2 text-[var(--fg-muted)]">
                      <Check className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--success)]" aria-hidden /><span>{b}</span>
                    </li>
                  ))}
                </ul>
                <div className="mt-5">
                  {isCustomPlan(p) ? (
                    <a href={SALES_MAILTO}
                      className="inline-flex h-8 w-full items-center justify-center gap-1.5 rounded-[var(--radius-sm)] border border-[var(--border)] text-[13px] font-medium text-[var(--fg)] transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--surface-2)]">
                      <Mail className="h-4 w-4" aria-hidden />Холбогдох
                    </a>
                  ) : current ? (
                    <Button variant="secondary" className="w-full" disabled>Идэвхтэй</Button>
                  ) : p.code === TRIAL_CODE ? (
                    <Button variant="secondary" className="w-full" disabled>Зөвхөн шинэ бүртгэлд</Button>
                  ) : (
                    <Button className="w-full" onClick={() => setSelected(p)} aria-label={`${p.name} багц сонгох`}>Сонгох</Button>
                  )}
                </div>
              </Card>
            )
          })}
        </div>
      )}
      <ConfirmDialog open={!!selected} onClose={() => setSelected(null)} tone="primary" confirmLabel="Баталгаажуулах"
        title={selected ? `"${selected.name}" багц руу шилжих үү?` : ''}
        description={selected && selected.monthlyMnt > 0
          ? `Сарын төлбөр ${fmtMnt(selected.monthlyMnt)} (НӨАТ ороогүй). Эхний сарын нэхэмжлэх үүсч, төлбөр төлөгдсөнөөр багц идэвхжинэ.`
          : 'Багц шууд солигдоно.'}
        onConfirm={confirmChange} />
      <PayDialog invoice={payInvoice} onClose={() => setPayInvoice(null)} />
    </div>
  )
}
