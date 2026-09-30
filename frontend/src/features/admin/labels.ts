import type { BadgeTone } from '@/components/ui'
import type { InvoiceStatus, OrgStatus, SubscriptionStatus } from '@/lib/types'

export const ORG_STATUS: Record<OrgStatus, { label: string; tone: BadgeTone }> = {
  active: { label: 'Идэвхтэй', tone: 'success' },
  suspended: { label: 'Түр хаагдсан', tone: 'warning' },
  closed: { label: 'Хаагдсан', tone: 'danger' },
}
export const SUB_STATUS: Record<SubscriptionStatus, { label: string; tone: BadgeTone }> = {
  trialing: { label: 'Туршилт', tone: 'info' },
  active: { label: 'Идэвхтэй', tone: 'success' },
  past_due: { label: 'Төлбөр хоцорсон', tone: 'warning' },
  canceled: { label: 'Цуцлагдсан', tone: 'neutral' },
}
export const INVOICE_STATUS: Record<InvoiceStatus, { label: string; tone: BadgeTone }> = {
  draft: { label: 'Ноорог', tone: 'neutral' },
  open: { label: 'Төлөгдөөгүй', tone: 'warning' },
  paid: { label: 'Төлсөн', tone: 'success' },
  void: { label: 'Хүчингүй', tone: 'neutral' },
}
export const SUB_STATUS_OPTIONS = (Object.keys(SUB_STATUS) as SubscriptionStatus[]).map((v) => ({ value: v, label: SUB_STATUS[v].label }))
