import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'outline'
export type ButtonSize = 'sm' | 'md' | 'lg' | 'icon'
export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant; size?: ButtonSize; loading?: boolean
}
const variants: Record<ButtonVariant, string> = {
  primary:
    'bg-[var(--accent)] text-[var(--fg-on-accent)] hover:bg-[var(--accent-hover)] shadow-[var(--shadow-sm)] shadow-[inset_0_1px_0_rgb(255_255_255/0.12)]',
  secondary:
    'bg-[var(--surface-2)] text-[var(--fg)] hover:bg-[var(--surface-3)] border border-[var(--border)] hover:border-[var(--border-strong)]',
  outline:
    'border border-[var(--border)] text-[var(--fg)] bg-transparent hover:bg-[var(--surface-2)] hover:border-[var(--border-strong)]',
  ghost: 'text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-2)]',
  danger: 'bg-[var(--danger-solid)] text-white hover:bg-[var(--danger-solid-hover)] shadow-[var(--shadow-sm)]',
}
const sizes: Record<ButtonSize, string> = {
  sm: 'h-7 px-2.5 text-xs gap-1.5 [&_svg]:size-3.5',
  md: 'h-8 px-3 text-[13px] gap-1.5 [&_svg]:size-4',
  lg: 'h-10 px-4 text-sm gap-2 [&_svg]:size-4',
  icon: 'h-8 w-8 p-0 [&_svg]:size-4',
}
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = 'primary', size = 'md', loading, disabled, children, ...rest }, ref) => (
    <button ref={ref} disabled={disabled || loading} aria-busy={loading || undefined}
      className={cn(
        'inline-flex shrink-0 select-none items-center justify-center whitespace-nowrap rounded-[var(--radius-sm)] font-medium leading-none transition-[background-color,border-color,color,box-shadow] duration-100',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--focus-ring)] focus-visible:ring-offset-1 focus-visible:ring-offset-[var(--surface-0)]',
        'disabled:pointer-events-none disabled:opacity-50 cursor-pointer',
        variants[variant], sizes[size], className,
      )}
      {...rest}>
      {loading && <Loader2 className="animate-spin" aria-hidden />}
      {children}
    </button>
  ),
)
Button.displayName = 'Button'
