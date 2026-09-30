import { useEffect, useRef } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, getToken, HttpError } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type { Invoice, Payment, PaymentStatus, Plan, Subscription, UsageSummary } from '@/lib/types'

// ---- response shapes (docs/API.md "Billing") ----
export interface SubscriptionResponse { subscription: Subscription; plan: Plan; usage: UsageSummary; limits: Plan }
export interface DailyUsage { day: string; minutes: number; calls: number; costMnt: number }
export interface UsageResponse { summary: UsageSummary; daily: DailyUsage[] }
export interface ChangePlanResponse { subscription: Subscription; invoice?: Invoice | null }
export interface InvoiceDetail { invoice: Invoice; payments: Payment[] }
export type PaymentProvider = 'qpay' | 'mock'
export interface QuotaWarningPayload { used: number; limit: number; percent: number }
export interface BillingUpdatedPayload { subscription?: Subscription; usage?: UsageSummary }

export const billingKeys = {
  all: ['billing'] as const,
  plans: ['billing', 'plans'] as const,
  subscription: ['billing', 'subscription'] as const,
  usage: (from?: string, to?: string) => ['billing', 'usage', from ?? '', to ?? ''] as const,
  invoices: ['billing', 'invoices'] as const,
  invoice: (id: string) => ['billing', 'invoice', id] as const,
  payment: (id: string) => ['billing', 'payment', id] as const,
}

export const PAYMENT_POLL_MS = 3000
const TERMINAL: PaymentStatus[] = ['paid', 'expired', 'failed']
export function isTerminalPayment(status?: PaymentStatus): boolean {
  return !!status && TERMINAL.includes(status)
}

export function usePlans() {
  return useQuery({
    queryKey: billingKeys.plans, staleTime: 5 * 60_000,
    queryFn: async () => ((await api.get<{ items: Plan[] }>('/billing/plans')).items ?? []).filter((p) => p.public),
  })
}

export function useSubscription(opts: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: billingKeys.subscription, staleTime: 30_000, enabled: opts.enabled ?? true,
    queryFn: () => api.get<SubscriptionResponse>('/billing/subscription'),
  })
}

export function useUsage(from?: string, to?: string) {
  return useQuery({
    queryKey: billingKeys.usage(from, to), enabled: !!from && !!to, staleTime: 60_000,
    queryFn: async () => {
      const res = await api.get<UsageResponse>('/billing/usage', { from, to })
      return { ...res, daily: res.daily ?? [] }
    },
  })
}

export function useInvoices() {
  return useQuery({
    queryKey: billingKeys.invoices,
    queryFn: async () => (await api.get<{ items: Invoice[] }>('/billing/invoices')).items ?? [],
  })
}

export function useInvoice(id: string | null | undefined) {
  return useQuery({
    queryKey: billingKeys.invoice(id ?? ''), enabled: !!id,
    queryFn: async () => {
      const res = await api.get<InvoiceDetail>(`/billing/invoices/${id}`)
      return { ...res, payments: res.payments ?? [] }
    },
  })
}

function invalidateAccount(qc: ReturnType<typeof useQueryClient>) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: billingKeys.subscription }),
    qc.invalidateQueries({ queryKey: billingKeys.invoices }),
    qc.invalidateQueries({ queryKey: ['billing', 'invoice'] }),
    qc.invalidateQueries({ queryKey: ['billing', 'usage'] }),
  ])
}

export function useChangePlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (planCode: string) => api.post<ChangePlanResponse>('/billing/subscription', { planCode }),
    onSuccess: () => invalidateAccount(qc),
  })
}

export function useCancelPlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.post<{ subscription: Subscription }>('/billing/subscription/cancel'),
    onSuccess: () => qc.invalidateQueries({ queryKey: billingKeys.subscription }),
  })
}

export function usePay() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ invoiceId, provider }: { invoiceId: string; provider: PaymentProvider }) =>
      (await api.post<{ payment: Payment }>(`/billing/invoices/${invoiceId}/pay`, { provider })).payment,
    onSuccess: (payment) => { qc.setQueryData(billingKeys.payment(payment.id), payment) },
  })
}

/** Polls a payment every 3 s while it is pending (also in background tabs: the user is in the bank app). */
export function usePayment(id: string | null | undefined) {
  return useQuery({
    queryKey: billingKeys.payment(id ?? ''), enabled: !!id,
    staleTime: PAYMENT_POLL_MS - 500,
    queryFn: async () => (await api.get<{ payment: Payment }>(`/billing/payments/${id}`)).payment,
    refetchInterval: (q) => (isTerminalPayment(q.state.data?.status) ? false : PAYMENT_POLL_MS),
    refetchIntervalInBackground: true,
  })
}

/** Invalidates billing queries on `billing.updated` (payments, period rollover). */
export function useBillingLiveSync(onUpdated?: (p: BillingUpdatedPayload) => void) {
  const qc = useQueryClient()
  const onEvent = useLive((s) => s.onEvent)
  const cb = useRef(onUpdated)
  useEffect(() => { cb.current = onUpdated }, [onUpdated])
  useEffect(() => onEvent((ev) => {
    if (ev.type !== 'billing.updated') return
    const p = (ev.payload ?? {}) as BillingUpdatedPayload
    qc.setQueryData<SubscriptionResponse>(billingKeys.subscription, (old) => old && {
      ...old, subscription: p.subscription ?? old.subscription, usage: p.usage ?? old.usage,
    })
    void qc.invalidateQueries({ queryKey: billingKeys.all })
    cb.current?.(p)
  }), [onEvent, qc])
}

/**
 * Opens the invoice PDF in a new tab. The endpoint needs the bearer token, so
 * it is fetched as a blob and shown through an object URL. The tab is opened
 * synchronously (inside the click) so popup blockers let it through.
 */
export async function openInvoicePdf(invoiceId: string): Promise<void> {
  const win = window.open('', '_blank')
  try {
    const headers: Record<string, string> = {}
    const token = getToken()
    if (token) headers.Authorization = `Bearer ${token}`
    const res = await fetch(`/api/billing/invoices/${invoiceId}/pdf`, { headers })
    if (!res.ok) throw new HttpError(res.status, 'http_error', res.statusText || 'PDF татахад алдаа гарлаа')
    const blob = await res.blob()
    const url = URL.createObjectURL(blob.type ? blob : new Blob([blob], { type: 'application/pdf' }))
    if (win) {
      win.opener = null
      win.location.href = url
    } else {
      const a = document.createElement('a')
      a.href = url; a.target = '_blank'; a.rel = 'noopener'
      document.body.appendChild(a); a.click(); a.remove()
    }
    setTimeout(() => URL.revokeObjectURL(url), 60_000)
  } catch (e) {
    win?.close()
    throw e
  }
}
