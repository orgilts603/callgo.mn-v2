import { useEffect, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type {
  AdminOrgRow, AdminStats, Invoice, Organization, OrgStatus, Plan, Subscription, SubscriptionStatus, UsageSummary, User,
} from '@/lib/types'

export const PAGE_SIZE = 20

export interface AdminOrgList { items: AdminOrgRow[]; total?: number }
export interface AdminOrgDetail {
  org: Organization; subscription?: Subscription | null; plan?: Plan | null; usage?: UsageSummary | null
  /** Member count (or the member list, depending on backend version). */
  users?: number | User[]; invoices?: Invoice[]
}
export interface SubscriptionBody {
  planCode: string; status?: SubscriptionStatus; customLimits?: Partial<Plan>; currentPeriodEnd?: string
}

export const statsKey = ['admin', 'stats'] as const
export const orgsKey = (q: string, page: number) => ['admin', 'orgs', q, page] as const
export const orgKey = (id: string) => ['admin', 'org', id] as const
export const plansKey = ['billing', 'plans'] as const

export const adminApi = {
  stats: () => api.get<AdminStats>('/admin/stats'),
  orgs: (q: string, page: number) => api.get<AdminOrgList>('/admin/orgs', { q: q || undefined, limit: PAGE_SIZE, offset: page * PAGE_SIZE }),
  org: (id: string) => api.get<AdminOrgDetail>(`/admin/orgs/${encodeURIComponent(id)}`),
  putSubscription: (id: string, body: SubscriptionBody) => api.put<{ subscription: Subscription }>(`/admin/orgs/${encodeURIComponent(id)}/subscription`, body),
  setStatus: (id: string, status: OrgStatus) => api.put<{ org: Organization }>(`/admin/orgs/${encodeURIComponent(id)}`, { status }),
  markPaid: (invoiceId: string, note: string) => api.post<{ invoice: Invoice }>(`/admin/invoices/${encodeURIComponent(invoiceId)}/mark-paid`, { note }),
  plans: () => api.get<{ items: Plan[] }>('/billing/plans'),
}

export function useAdminStats() {
  return useQuery({ queryKey: statsKey, queryFn: adminApi.stats })
}

export function useAdminOrgs(q: string, page: number) {
  return useQuery({ queryKey: orgsKey(q, page), queryFn: () => adminApi.orgs(q, page), placeholderData: keepPreviousData })
}

export function useAdminOrg(id: string | null) {
  return useQuery({ queryKey: orgKey(id ?? ''), queryFn: () => adminApi.org(id as string), enabled: !!id })
}

export function usePlans() {
  return useQuery({ queryKey: plansKey, queryFn: async () => (await adminApi.plans()).items, staleTime: 5 * 60_000 })
}

/** Refresh the orgs table, stats and the open org after any admin mutation. */
export function useInvalidateAdmin() {
  const qc = useQueryClient()
  return (orgId?: string) => {
    void qc.invalidateQueries({ queryKey: ['admin', 'orgs'] })
    void qc.invalidateQueries({ queryKey: statsKey })
    if (orgId) void qc.invalidateQueries({ queryKey: orgKey(orgId) })
  }
}

export function useAdminMutations(orgId: string) {
  const invalidate = useInvalidateAdmin()
  const onSuccess = () => invalidate(orgId)
  return {
    saveSubscription: useMutation({ mutationFn: (b: SubscriptionBody) => adminApi.putSubscription(orgId, b), onSuccess }),
    setStatus: useMutation({ mutationFn: (s: OrgStatus) => adminApi.setStatus(orgId, s), onSuccess }),
    markPaid: useMutation({ mutationFn: (v: { invoiceId: string; note: string }) => adminApi.markPaid(v.invoiceId, v.note), onSuccess }),
  }
}

/** Local debounce (kept here so the admin feature has no cross-feature dependency). */
export function useDebounced<T>(value: T, ms = 300): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}
