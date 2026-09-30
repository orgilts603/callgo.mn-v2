import { format, formatDistanceToNowStrict } from 'date-fns'
import { mn } from 'date-fns/locale/mn'

/** Ratios arrive as 0..1; tolerate 0..100 too. */
export function fmtPercent(ratio: number | undefined | null): string {
  if (ratio == null || Number.isNaN(ratio)) return '—'
  const pct = ratio <= 1 ? ratio * 100 : ratio
  return `${pct >= 10 || pct === 0 ? Math.round(pct) : pct.toFixed(1)}%`
}

export function fmtNumber(n: number | undefined | null): string {
  if (n == null) return '—'
  return new Intl.NumberFormat('en-US').format(n).replace(/,/g, ' ')
}

export function fmtAgoMn(iso?: string | null): string {
  if (!iso) return '—'
  try { return formatDistanceToNowStrict(new Date(iso), { addSuffix: true, locale: mn }) } catch { return '—' }
}

/** "2026-09-30" or a full ISO timestamp → "09/30" (calendar day as sent, no TZ shift). */
export function fmtDay(iso: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso)
  if (m) return `${m[2]}/${m[3]}`
  try { return format(new Date(iso), 'MM/dd') } catch { return iso }
}
