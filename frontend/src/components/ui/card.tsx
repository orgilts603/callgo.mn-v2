import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

export function Card({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('rounded-[var(--radius-lg)] border border-[var(--border)] border-t-[var(--border-highlight)] bg-[var(--surface-1)] shadow-[var(--shadow-sm)]', className)} {...rest} />
}
export function CardHeader({ title, description, actions, className }: { title: ReactNode; description?: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex items-center justify-between gap-4 border-b border-[var(--border-subtle)] px-4 py-3', className)}>
      <div className="min-w-0">
        <h3 className="truncate text-[13px] font-semibold tracking-[-0.005em] text-[var(--fg)]">{title}</h3>
        {description && <p className="mt-0.5 text-xs text-[var(--fg-muted)]">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}
export function CardBody({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('px-4 py-4', className)} {...rest} />
}
export function StatCard({ label, value, hint, icon, className }: { label: string; value: ReactNode; hint?: ReactNode; icon?: ReactNode; className?: string }) {
  return (
    <Card className={cn('px-4 py-3.5', className)}>
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-[11px] font-medium uppercase tracking-[0.04em] text-[var(--fg-muted)]">{label}</span>
        {icon && <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-[var(--surface-2)] text-[var(--fg-muted)] [&_svg]:size-3.5">{icon}</span>}
      </div>
      <div className="mt-2 font-mono text-[22px] font-medium leading-7 tracking-tight tabular-nums text-[var(--fg)]">{value}</div>
      {hint && <div className="mt-1 truncate text-xs text-[var(--fg-subtle)]">{hint}</div>}
    </Card>
  )
}
