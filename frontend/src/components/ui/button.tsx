import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'outline'
export type ButtonSize = 'sm' | 'md' | 'lg' | 'icon'
export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant; size?: ButtonSize; loading?: boolean
}
const variants: Record<ButtonVariant, string> = {
  primary: 'bg-[var(--accent)] text-white hover:bg-[var(--accent-hover)] shadow-sm',
  secondary: 'bg-[var(--surface-2)] text-[var(--fg)] hover:bg-[var(--surface-3)] border border-[var(--border)]',
  outline: 'border border-[var(--border)] text-[var(--fg)] hover:bg-[var(--surface-2)]',
  ghost: 'text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-2)]',
  danger: 'bg-red-600/90 text-white hover:bg-red-600',
}
const sizes: Record<ButtonSize, string> = {
  sm: 'h-8 px-3 text-xs gap-1.5', md: 'h-9 px-4 text-sm gap-2', lg: 'h-11 px-6 text-base gap-2', icon: 'h-9 w-9 p-0',
}
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = 'primary', size = 'md', loading, disabled, children, ...rest }, ref) => (
    <button ref={ref} disabled={disabled || loading}
      className={cn('inline-flex items-center justify-center rounded-md font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] disabled:opacity-50 disabled:pointer-events-none cursor-pointer', variants[variant], sizes[size], className)}
      {...rest}>
      {loading && <Loader2 className="h-4 w-4 animate-spin" />}
      {children}
    </button>
  ),
)
Button.displayName = 'Button'
