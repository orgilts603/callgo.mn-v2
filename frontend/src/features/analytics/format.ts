export { fmtNumber, fmtPercent, fmtDay } from '@/features/dashboard/format'

/** Money in Mongolian tugrik: "12 500 ₮". */
export function fmtMnt(n: number | undefined | null): string {
  if (n == null || Number.isNaN(n)) return '—'
  return `${new Intl.NumberFormat('en-US').format(Math.round(n)).replace(/,/g, ' ')} ₮`
}

/** Minutes with at most one decimal. */
export function fmtMinutes(n: number | undefined | null): string {
  if (n == null || Number.isNaN(n)) return '—'
  const v = Math.round(n * 10) / 10
  return new Intl.NumberFormat('en-US', { maximumFractionDigits: 1 }).format(v).replace(/,/g, ' ')
}
