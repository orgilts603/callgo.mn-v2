import { format } from 'date-fns'
import type { InvoiceStatus, Plan, SubscriptionStatus } from '@/lib/types'
import type { BadgeTone } from '@/components/ui'

const DAY_MS = 86_400_000

/** Groups thousands with a plain space: 1290000 → "1 290 000". */
export function fmtNum(n: number, fractionDigits = 0): string {
  if (!Number.isFinite(n)) return '0'
  const factor = 10 ** fractionDigits
  const rounded = Math.round(n * factor) / factor
  const neg = rounded < 0
  const [int, frac] = Math.abs(rounded).toFixed(fractionDigits).split('.')
  const grouped = int.replace(/\B(?=(\d{3})+(?!\d))/g, ' ')
  const trimmedFrac = frac?.replace(/0+$/, '')
  return `${neg ? '-' : ''}${grouped}${trimmedFrac ? `.${trimmedFrac}` : ''}`
}

/** MNT amount: 1290000 → "1 290 000 ₮". */
export function fmtMnt(n: number): string {
  return `${fmtNum(n)} ₮`
}

export function fmtDate(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '—' : format(d, 'yyyy-MM-dd')
}

export function fmtPeriod(start?: string | null, end?: string | null): string {
  return `${fmtDate(start)} – ${fmtDate(end)}`
}

/** Whole days left until `iso` (rounded up, never negative). */
export function daysUntil(iso: string | null | undefined, now: number = Date.now()): number | null {
  if (!iso) return null
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return null
  return Math.max(0, Math.ceil((t - now) / DAY_MS))
}

/** mm:ss countdown, "0:00" once past. */
export function fmtCountdown(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

export type MeterTone = 'success' | 'warning' | 'danger'
/** Usage meter colour: emerald below 80 %, amber 80–100 %, rose above 100 %. */
export function meterTone(percent: number): MeterTone {
  if (percent > 100) return 'danger'
  if (percent >= 80) return 'warning'
  return 'success'
}
export const meterBarClass: Record<MeterTone, string> = {
  success: 'bg-[var(--success)]',
  warning: 'bg-[var(--warning)]',
  danger: 'bg-[var(--danger)]',
}

export function usagePercent(used: number, included: number): number {
  return included > 0 ? (used / included) * 100 : 0
}

export const subStatusLabel: Record<SubscriptionStatus, string> = {
  trialing: 'Туршилт', active: 'Идэвхтэй', past_due: 'Төлбөр хүлээгдэж буй', canceled: 'Цуцлагдсан',
}
export const subStatusTone: Record<SubscriptionStatus, BadgeTone> = {
  trialing: 'info', active: 'success', past_due: 'danger', canceled: 'neutral',
}

export const invoiceStatusLabel: Record<InvoiceStatus, string> = { draft: 'Ноорог', open: 'Төлөгдөөгүй', paid: 'Төлөгдсөн', void: 'Хүчингүй' }
export const invoiceStatusTone: Record<InvoiceStatus, BadgeTone> = { draft: 'neutral', open: 'warning', paid: 'success', void: 'neutral' }

export const featureLabel: Record<string, string> = {
  recordings: 'Дуудлагын бичлэг',
  webhooks: 'Webhook интеграц',
  sms: 'SMS илгээх',
  analytics: 'Дэлгэрэнгүй аналитик',
  api: 'API түлхүүр',
  handoff: 'Оператор руу шилжүүлэх',
  priority_support: 'Тэргүүн ээлжийн дэмжлэг',
}

export const ENTERPRISE_CODE = 'enterprise'
export const TRIAL_CODE = 'trial'
export const SALES_MAILTO = 'mailto:sales@callgo.mn?subject=Enterprise%20%D0%B1%D0%B0%D0%B3%D1%86'

export function isCustomPlan(p: Pick<Plan, 'code'>): boolean {
  return p.code === ENTERPRISE_CODE
}

/** Plan cap with "0 = unlimited" semantics. */
export function fmtLimit(n: number, unit = ''): string {
  return n > 0 ? `${fmtNum(n)}${unit ? ` ${unit}` : ''}` : 'Хязгааргүй'
}

/** Human-readable feature bullets for a pricing card. */
export function planBullets(p: Plan): string[] {
  const custom = isCustomPlan(p)
  const out: string[] = []
  out.push(custom && p.includedMinutes <= 0 ? 'Минут: тохиролцоно' : `${fmtNum(p.includedMinutes)} минут багтсан`)
  if (!custom || p.maxConcurrentCalls > 0) out.push(`${p.maxConcurrentCalls > 0 ? fmtNum(p.maxConcurrentCalls) : 'Хязгааргүй'} зэрэг дуудлага`)
  out.push(`${fmtLimit(p.maxAgentProfiles)} агент профайл`)
  out.push(`${fmtLimit(p.maxUsers)} хэрэглэгч`)
  out.push(`Мэдлэгийн сан: ${p.maxKnowledgeMb > 0 ? `${fmtNum(p.maxKnowledgeMb)} MB` : 'Хязгааргүй'}`)
  if (p.overageMntPerMin > 0) out.push(`Нэмэлт минут: ${fmtMnt(p.overageMntPerMin)}/мин`)
  else if (!custom) out.push('Нэмэлт минут: боломжгүй')
  for (const f of p.features ?? []) out.push(featureLabel[f] ?? f)
  return out
}

/** Provider QR images are raw base64 PNG (QPay) or a full URL / data URI. */
export function qrImageSrc(qrImage: string): string {
  return /^(data:|https?:|\/)/.test(qrImage) ? qrImage : `data:image/png;base64,${qrImage}`
}
