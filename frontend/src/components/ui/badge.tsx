import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export type BadgeTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger' | 'accent'
export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> { tone?: BadgeTone; dot?: boolean; pulse?: boolean }
const tones: Record<BadgeTone, string> = {
  neutral: 'bg-[var(--surface-2)] text-[var(--fg-muted)] border-[var(--border)]',
  info: 'bg-sky-500/10 text-sky-300 border-sky-500/30',
  success: 'bg-emerald-500/10 text-emerald-300 border-emerald-500/30',
  warning: 'bg-amber-500/10 text-amber-300 border-amber-500/30',
  danger: 'bg-red-500/10 text-red-300 border-red-500/30',
  accent: 'bg-[var(--accent)]/15 text-[var(--accent-fg)] border-[var(--accent)]/40',
}
const dotColor: Record<BadgeTone, string> = {
  neutral: 'bg-zinc-400', info: 'bg-sky-400', success: 'bg-emerald-400', warning: 'bg-amber-400', danger: 'bg-red-400', accent: 'bg-[var(--accent)]',
}
export function Badge({ className, tone = 'neutral', dot, pulse, children, ...rest }: BadgeProps) {
  return (
    <span className={cn('inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-medium leading-4 whitespace-nowrap', tones[tone], className)} {...rest}>
      {dot && (
        <span className="relative flex h-1.5 w-1.5">
          {pulse && <span className={cn('absolute inline-flex h-full w-full animate-ping rounded-full opacity-75', dotColor[tone])} />}
          <span className={cn('relative inline-flex h-1.5 w-1.5 rounded-full', dotColor[tone])} />
        </span>
      )}
      {children}
    </span>
  )
}
