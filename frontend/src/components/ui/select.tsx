import { forwardRef, type SelectHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'
import { inputClass } from './input'

export interface SelectOption { value: string; label: string }
export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> { options?: SelectOption[]; placeholder?: string }
export const Select = forwardRef<HTMLSelectElement, SelectProps>(({ className, options, placeholder, children, ...rest }, ref) => (
  <select ref={ref} className={cn(inputClass, 'appearance-none pr-8', className)} {...rest}>
    {placeholder && <option value="">{placeholder}</option>}
    {options?.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    {children}
  </select>
))
Select.displayName = 'Select'
