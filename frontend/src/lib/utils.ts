import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'
import { format, formatDistanceToNowStrict } from 'date-fns'

export function cn(...inputs: ClassValue[]) { return twMerge(clsx(inputs)) }

export function fmtDuration(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  const m = Math.floor(s / 60)
  const h = Math.floor(m / 60)
  const mm = String(m % 60).padStart(2, '0')
  const ss = String(s % 60).padStart(2, '0')
  return h > 0 ? `${h}:${mm}:${ss}` : `${m}:${ss}`
}
export function fmtDateTime(iso?: string | null): string { return iso ? format(new Date(iso), 'yyyy-MM-dd HH:mm') : '—' }
export function fmtAgo(iso?: string | null): string { return iso ? formatDistanceToNowStrict(new Date(iso), { addSuffix: true }) : '—' }
export function fmtPhone(p: string): string {
  const m = /^\+976(\d{4})(\d{4})$/.exec(p)
  return m ? `+976 ${m[1]} ${m[2]}` : p
}
