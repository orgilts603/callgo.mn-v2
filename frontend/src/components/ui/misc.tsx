import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

export function Skeleton({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('animate-pulse rounded-md bg-[var(--surface-2)]', className)} {...rest} />
}
export function EmptyState({ icon, title, description, action, className }: { icon?: ReactNode; title: ReactNode; description?: ReactNode; action?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex flex-col items-center justify-center gap-2 py-16 text-center', className)}>
      {icon && <div className="mb-1 text-[var(--fg-subtle)]">{icon}</div>}
      <div className="text-sm font-medium text-[var(--fg)]">{title}</div>
      {description && <div className="max-w-sm text-xs text-[var(--fg-muted)]">{description}</div>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  )
}
export function PageHeader({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex items-start justify-between gap-4">
      <div>
        <h1 className="text-xl font-semibold tracking-tight text-[var(--fg)]">{title}</h1>
        {description && <p className="mt-1 text-sm text-[var(--fg-muted)]">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}
export function Spinner({ className }: { className?: string }) {
  return <div className={cn('h-5 w-5 animate-spin rounded-full border-2 border-[var(--border)] border-t-[var(--accent)]', className)} />
}
