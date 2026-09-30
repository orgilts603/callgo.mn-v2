import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

export function Skeleton({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div aria-hidden className={cn('animate-pulse rounded-md bg-[var(--surface-2)]', className)} {...rest} />
}
export function EmptyState({ icon, title, description, action, className }: { icon?: ReactNode; title: ReactNode; description?: ReactNode; action?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex flex-col items-center justify-center gap-1.5 px-6 py-14 text-center', className)}>
      {icon && <div className="mb-2 flex h-10 w-10 items-center justify-center rounded-[var(--radius)] border border-[var(--border)] bg-[var(--surface-2)] text-[var(--fg-muted)] [&_svg]:size-[18px]">{icon}</div>}
      <div className="text-[13px] font-medium text-[var(--fg)]">{title}</div>
      {description && <div className="max-w-sm text-xs leading-relaxed text-[var(--fg-muted)]">{description}</div>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  )
}
export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-x-4 gap-y-3">
      <div className="min-w-0">
        <h1 className="text-lg font-semibold tracking-[-0.015em] text-[var(--fg)]">{title}</h1>
        {description && <p className="mt-1 text-[13px] text-[var(--fg-muted)]">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}
export function Spinner({ className }: { className?: string }) {
  return <div role="status" aria-label="Loading" className={cn('h-5 w-5 animate-spin rounded-full border-2 border-[var(--border)] border-t-[var(--accent)]', className)} />
}
