import { forwardRef, type TextareaHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'
import { inputClass } from './input'

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(({ className, ...rest }, ref) => (
  <textarea ref={ref} className={cn(inputClass, 'h-auto min-h-24 resize-y py-2 leading-relaxed', className)} {...rest} />
))
Textarea.displayName = 'Textarea'
