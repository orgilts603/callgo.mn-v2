import { useSearchParams } from 'react-router-dom'
import { cn } from '@/lib/utils'
import { useBillingLiveSync } from './hooks'
import { PlansGrid } from './PlansGrid'
import { UsageMeters } from './UsageMeters'
import { InvoicesTable } from './InvoicesTable'

const BILLING_VIEWS = [
  { id: 'plans', label: 'Багц' },
  { id: 'usage', label: 'Хэрэглээ' },
  { id: 'invoices', label: 'Нэхэмжлэх' },
] as const
type BillingView = (typeof BILLING_VIEWS)[number]['id']

function isView(v: string | null): v is BillingView {
  return BILLING_VIEWS.some((x) => x.id === v)
}

/** Billing: plans, usage and invoices. Sub-tab lives in `?view=` so it composes with the /settings/:tab route. */
export default function BillingPage() {
  const [params, setParams] = useSearchParams()
  const raw = params.get('view')
  const view: BillingView = isView(raw) ? raw : 'plans'
  useBillingLiveSync()

  const select = (id: BillingView) => {
    setParams((p) => {
      const next = new URLSearchParams(p)
      if (id === 'plans') next.delete('view')
      else next.set('view', id)
      return next
    }, { replace: true })
  }

  return (
    <div>
      <div role="tablist" aria-label="Төлбөрийн хэсгүүд"
        className="mb-4 inline-flex rounded-[var(--radius-sm)] border border-[var(--border)] bg-[var(--surface-inset)] p-0.5">
        {BILLING_VIEWS.map((v) => (
          <button key={v.id} type="button" role="tab" id={`billing-tab-${v.id}`} aria-selected={view === v.id} aria-controls={`billing-panel-${v.id}`}
            onClick={() => select(v.id)}
            className={cn('h-7 rounded-[4px] px-3 text-[13px] font-medium transition-colors',
              view === v.id ? 'bg-[var(--surface-3)] text-[var(--fg)] shadow-[var(--shadow-sm)]' : 'text-[var(--fg-muted)] hover:text-[var(--fg)]')}>
            {v.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`billing-panel-${view}`} aria-labelledby={`billing-tab-${view}`}>
        {view === 'plans' && <PlansGrid />}
        {view === 'usage' && <UsageMeters />}
        {view === 'invoices' && <InvoicesTable />}
      </div>
    </div>
  )
}
