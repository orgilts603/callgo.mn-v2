import type { OrgStatus } from '@/lib/types'
import type { SubscriptionResponse } from './hooks'
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
