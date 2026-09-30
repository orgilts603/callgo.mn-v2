import type { HTMLAttributes, ReactNode } from 'react'
import { cn } from '@/lib/utils'

export function Card({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('rounded-xl border border-[var(--border)] bg-[var(--surface-1)] shadow-sm', className)} {...rest} />
}
export function CardHeader({ title, description, actions, className }: { title: ReactNode; description?: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <div className={cn('flex items-start justify-between gap-4 border-b border-[var(--border)] px-5 py-4', className)}>
      <div>
        <h3 className="text-sm font-semibold text-[var(--fg)]">{title}</h3>
        {description && <p className="mt-0.5 text-xs text-[var(--fg-muted)]">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}
export function CardBody({ className, ...rest }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('px-5 py-4', className)} {...rest} />
}
export function StatCard({ label, value, hint, icon, className }: { label: string; value: ReactNode; hint?: ReactNode; icon?: ReactNode; className?: string }) {
  return (
    <Card className={cn('px-5 py-4', className)}>
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium uppercase tracking-wide text-[var(--fg-muted)]">{label}</span>
        {icon && <span className="text-[var(--fg-muted)]">{icon}</span>}
      </div>
      <div className="mt-2 text-2xl font-semibold tabular-nums text-[var(--fg)]">{value}</div>
      {hint && <div className="mt-1 text-xs text-[var(--fg-muted)]">{hint}</div>}
    </Card>
  )
}
