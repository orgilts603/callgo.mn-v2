import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { AlertTriangle, Clock, X } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/app/auth'
import { useLive } from '@/lib/ws'
import { cn } from '@/lib/utils'
import type { OrgStatus } from '@/lib/types'
import { billingKeys, useBillingLiveSync, useSubscription, type QuotaWarningPayload, type SubscriptionResponse } from './hooks'
import { daysUntil, fmtNum, usagePercent } from './format'

export const BILLING_PATH = '/settings/billing'
export const BILLING_INVOICES_PATH = `${BILLING_PATH}?view=invoices`
export const BILLING_USAGE_PATH = `${BILLING_PATH}?view=usage`

export type QuotaBannerKind = 'suspended' | 'past_due' | 'minutes_exceeded' | 'minutes_warning' | 'trial_ending'
export interface QuotaBannerItem { kind: QuotaBannerKind; tone: 'warning' | 'danger'; message: string; action?: { label: string; to: string } }

/** Pure: which banners apply to this org / subscription right now (most severe first). */
export function quotaBanners(data: SubscriptionResponse | undefined, orgStatus: OrgStatus | undefined, now: number = Date.now()): QuotaBannerItem[] {
  const out: QuotaBannerItem[] = []
  if (orgStatus === 'suspended') {
    out.push({ kind: 'suspended', tone: 'danger', message: 'Төлбөр төлөгдөөгүй тул байгууллагын эрх түр хаагдсан. Шинэ дуудлага, өөрчлөлт хийх боломжгүй.', action: { label: 'Нэхэмжлэх төлөх', to: BILLING_INVOICES_PATH } })
  }
  if (!data) return out
  const { subscription: sub, usage, limits } = data
  if (sub.status === 'past_due' && orgStatus !== 'suspended') {
    out.push({ kind: 'past_due', tone: 'danger', message: 'Төлбөрийн хугацаа хэтэрсэн байна. Үйлчилгээ зогсохоос өмнө нэхэмжлэхээ төлнө үү.', action: { label: 'Нэхэмжлэх төлөх', to: BILLING_INVOICES_PATH } })
  }
  const included = usage.includedMinutes > 0 ? usage.includedMinutes : limits.includedMinutes
  const pct = usagePercent(usage.minutes, included)
  if (included > 0 && pct >= 100) {
    out.push({ kind: 'minutes_exceeded', tone: 'danger', message: `Багцын минут дууссан (${fmtNum(usage.minutes, 1)} / ${fmtNum(included)} мин).${limits.overageMntPerMin > 0 ? ' Нэмэлт минут тооцогдож байна.' : ' Шинэ дуудлага хаагдсан.'}`, action: { label: 'Багц ахиулах', to: BILLING_PATH } })
  } else if (included > 0 && pct >= 80) {
    out.push({ kind: 'minutes_warning', tone: 'warning', message: `Багцын минутын ${fmtNum(pct)}% ашиглагдлаа (${fmtNum(usage.minutes, 1)} / ${fmtNum(included)} мин).`, action: { label: 'Хэрэглээ харах', to: BILLING_USAGE_PATH } })
  }
  if (sub.status === 'trialing') {
    const days = daysUntil(sub.trialEndsAt, now)
    if (days !== null && days <= 3) {
      out.push({ kind: 'trial_ending', tone: 'warning', message: days > 0 ? `Туршилтын хугацаа ${days} хоногийн дараа дуусна.` : 'Туршилтын хугацаа дууссан.', action: { label: 'Багц сонгох', to: BILLING_PATH } })
    }
  }
  return out
}

const toneClass = {
  warning: 'border-[var(--warning-border)] bg-[var(--warning-soft)] text-[var(--warning-fg)]',
  danger: 'border-[var(--danger-border)] bg-[var(--danger-soft)] text-[var(--danger-fg)]',
} as const

/**
 * Account-level billing alerts for the app shell: trial ending, included
 * minutes at 80 % / 100 %, past-due and suspended. Toasts on `quota.warning`.
 * Purely informational — it never blocks navigation.
 */
export function QuotaBanner({ className }: { className?: string }) {
  const org = useAuth((s) => s.org)
  const token = useAuth((s) => s.token)
  const sub = useSubscription({ enabled: !!token })
  const qc = useQueryClient()
  const onEvent = useLive((s) => s.onEvent)
  const [dismissed, setDismissed] = useState<Set<QuotaBannerKind>>(() => new Set())
  useBillingLiveSync()

  useEffect(() => onEvent((ev) => {
    if (ev.type !== 'quota.warning') return
    const p = (ev.payload ?? {}) as Partial<QuotaWarningPayload>
    const percent = Math.round(p.percent ?? 0)
    const detail = p.limit ? ` (${fmtNum(p.used ?? 0, 1)} / ${fmtNum(p.limit)} мин)` : ''
    if (percent >= 100) toast.error(`Багцын минут дууслаа${detail}`)
    else toast.warning(`Багцын минутын ${percent}% ашиглагдлаа${detail}`)
    void qc.invalidateQueries({ queryKey: billingKeys.subscription })
  }), [onEvent, qc])

  const items = quotaBanners(sub.data, org?.status).filter((b) => !dismissed.has(b.kind))
  if (items.length === 0) return null
  return (
    <div className={cn('space-y-2', className)} data-testid="quota-banner">
      {items.map((b) => (
        <div key={b.kind} role={b.tone === 'danger' ? 'alert' : 'status'} data-kind={b.kind} data-tone={b.tone}
          className={cn('flex items-center gap-3 rounded-[var(--radius)] border px-3 py-2 text-[13px]', toneClass[b.tone])}>
          {b.kind === 'trial_ending' ? <Clock className="h-4 w-4 shrink-0" aria-hidden /> : <AlertTriangle className="h-4 w-4 shrink-0" aria-hidden />}
          <span className="min-w-0 flex-1">{b.message}</span>
          {b.action && <Link to={b.action.to} className="shrink-0 font-medium underline underline-offset-2 hover:opacity-80">{b.action.label}</Link>}
          {b.tone === 'warning' && (
            <button type="button" aria-label="Хаах" onClick={() => setDismissed((s) => new Set(s).add(b.kind))}
              className="shrink-0 rounded p-0.5 opacity-70 transition-opacity hover:opacity-100"><X className="h-3.5 w-3.5" /></button>
          )}
        </div>
      ))}
    </div>
  )
}

export default QuotaBanner
