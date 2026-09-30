import { forwardRef, type InputHTMLAttributes, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

export const inputClass =
  'h-8 w-full rounded-[var(--radius-sm)] border border-[var(--border)] bg-[var(--surface-inset)] px-2.5 text-[13px] text-[var(--fg)] placeholder:text-[var(--fg-subtle)] transition-[border-color,box-shadow] duration-100 hover:border-[var(--border-strong)] focus:outline-none focus-visible:outline-none focus:border-[var(--accent)] focus:ring-2 focus:ring-[var(--accent-soft)] disabled:cursor-not-allowed disabled:opacity-50 aria-[invalid=true]:border-[var(--danger-border)]'

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(({ className, ...rest }, ref) => (
  <input ref={ref} className={cn(inputClass, className)} {...rest} />
))
Input.displayName = 'Input'

export function Field({ label, hint, error, children, className }: { label: ReactNode; hint?: ReactNode; error?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cn('block space-y-1.5', className)}>
      <span className="block text-xs font-medium text-[var(--fg-muted)]">{label}</span>
      {children}
      {error ? <span className="block text-xs text-[var(--danger)]" role="alert">{error}</span> : hint ? <span className="block text-xs text-[var(--fg-subtle)]">{hint}</span> : null}
    </label>
  )
}
