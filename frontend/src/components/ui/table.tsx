import type { HTMLAttributes, TdHTMLAttributes, ThHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

export function Table({ className, ...rest }: HTMLAttributes<HTMLTableElement>) {
  return <div className="w-full overflow-x-auto"><table className={cn('w-full border-collapse text-sm', className)} {...rest} /></div>
}
export function THead({ className, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <thead className={cn('sticky top-0 z-10 bg-[var(--surface-1)] text-left text-[11px] uppercase tracking-wide text-[var(--fg-muted)]', className)} {...rest} />
}
export function TBody({ className, ...rest }: HTMLAttributes<HTMLTableSectionElement>) {
  return <tbody className={cn('divide-y divide-[var(--border)]', className)} {...rest} />
}
export function TR({ className, ...rest }: HTMLAttributes<HTMLTableRowElement>) {
  return <tr className={cn('transition-colors hover:bg-[var(--surface-2)]/60', className)} {...rest} />
}
export function TH({ className, ...rest }: ThHTMLAttributes<HTMLTableCellElement>) {
  return <th className={cn('border-b border-[var(--border)] px-3 py-2 font-medium', className)} {...rest} />
}
export function TD({ className, ...rest }: TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn('px-3 py-2 align-middle text-[var(--fg)]', className)} {...rest} />
}
