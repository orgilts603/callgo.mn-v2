import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

export const inputClass = 'h-9 w-full rounded-md border border-[var(--border)] bg-[var(--surface-0)] px-3 text-sm text-[var(--fg)] placeholder:text-[var(--fg-subtle)] focus:outline-none focus:ring-2 focus:ring-[var(--accent)] disabled:opacity-50'

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(({ className, ...rest }, ref) => (
  <input ref={ref} className={cn(inputClass, className)} {...rest} />
))
Input.displayName = 'Input'

export function Field({ label, hint, error, children, className }: { label: ReactNode; hint?: ReactNode; error?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cn('block space-y-1.5', className)}>
      <span className="text-xs font-medium text-[var(--fg-muted)]">{label}</span>
      {children}
      {error ? <span className="block text-xs text-red-400">{error}</span> : hint ? <span className="block text-xs text-[var(--fg-subtle)]">{hint}</span> : null}
    </label>
  )
}
