import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { AlertTriangle, Clock, X } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuth } from '@/app/auth'
import { useLive } from '@/lib/ws'
import { cn } from '@/lib/utils'
import { billingKeys, useBillingLiveSync, useSubscription, type QuotaWarningPayload } from './hooks'
import { fmtNum } from './format'
import { quotaBanners, type QuotaBannerKind } from './quota'

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
