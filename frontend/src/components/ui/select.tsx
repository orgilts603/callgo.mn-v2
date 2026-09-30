import { forwardRef, type SelectHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'
import { inputClass } from './input'

export interface SelectOption { value: string; label: string }
export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> { options?: SelectOption[]; placeholder?: string }

// Chevron drawn with an inline SVG background so <select> stays native (keyboard + a11y for free).
const chevron =
  "bg-[url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 24 24' fill='none' stroke='%239aa3c7' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E\")] bg-[length:12px_12px] bg-[position:right_0.6rem_center] bg-no-repeat"

export const Select = forwardRef<HTMLSelectElement, SelectProps>(({ className, options, placeholder, children, ...rest }, ref) => (
  <select ref={ref} className={cn(inputClass, chevron, 'cursor-pointer appearance-none pr-8 [&>option]:bg-[var(--surface-1)]', className)} {...rest}>
    {placeholder && <option value="">{placeholder}</option>}
    {options?.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    {children}
  </select>
))
Select.displayName = 'Select'
