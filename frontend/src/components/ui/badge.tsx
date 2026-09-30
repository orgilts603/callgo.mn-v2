import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export type BadgeTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger' | 'accent'
export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> { tone?: BadgeTone; dot?: boolean; pulse?: boolean }
const tones: Record<BadgeTone, string> = {
  neutral: 'bg-[var(--surface-2)] text-[var(--fg-muted)] border-[var(--border)]',
  info: 'bg-[var(--info-soft)] text-[var(--info-fg)] border-[var(--info-border)]',
  success: 'bg-[var(--success-soft)] text-[var(--success-fg)] border-[var(--success-border)]',
  warning: 'bg-[var(--warning-soft)] text-[var(--warning-fg)] border-[var(--warning-border)]',
  danger: 'bg-[var(--danger-soft)] text-[var(--danger-fg)] border-[var(--danger-border)]',
  accent: 'bg-[var(--accent-soft)] text-[var(--accent-fg)] border-[var(--accent-border)]',
}
const dotColor: Record<BadgeTone, string> = {
  neutral: 'bg-[var(--neutral-dot)]', info: 'bg-[var(--info)]', success: 'bg-[var(--success)]',
  warning: 'bg-[var(--warning)]', danger: 'bg-[var(--danger)]', accent: 'bg-[var(--accent)]',
}
export function Badge({ className, tone = 'neutral', dot, pulse, children, ...rest }: BadgeProps) {
  return (
    <span className={cn('inline-flex h-5 items-center gap-1.5 rounded-full border px-2 text-[11px] font-medium leading-none whitespace-nowrap', tones[tone], className)} {...rest}>
      {dot && (
        <span className="relative flex h-1.5 w-1.5 shrink-0" aria-hidden>
          {pulse && <span className={cn('absolute inline-flex h-full w-full animate-ping rounded-full opacity-75', dotColor[tone])} />}
          <span className={cn('relative inline-flex h-1.5 w-1.5 rounded-full', dotColor[tone])} />
        </span>
      )}
      {children}
    </span>
  )
}
