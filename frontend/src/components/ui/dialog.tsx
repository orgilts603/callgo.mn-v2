import { useEffect, useId, useRef, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useFocusTrap } from './use-focus-trap'

export interface DialogProps { open: boolean; onClose: () => void; title?: ReactNode; description?: ReactNode; children: ReactNode; footer?: ReactNode; className?: string }
export function Dialog({ open, onClose, title, description, children, footer, className }: DialogProps) {
  const panel = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const descId = useId()
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return () => { window.removeEventListener('keydown', onKey); document.body.style.overflow = overflow }
  }, [open, onClose])
  useFocusTrap(open, panel)
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto p-4 pt-[12vh]" role="dialog" aria-modal="true"
      aria-labelledby={title ? titleId : undefined} aria-describedby={description ? descId : undefined}>
      <div className="animate-fade-in fixed inset-0 bg-[var(--backdrop)] backdrop-blur-[2px]" onClick={onClose} />
      <div ref={panel} tabIndex={-1}
        className={cn('animate-pop-in relative w-full max-w-lg rounded-[var(--radius-lg)] border border-[var(--border)] bg-[var(--surface-overlay)] shadow-[var(--shadow-lg)] focus:outline-none', className)}>
        <div className="flex items-start justify-between gap-4 px-5 pt-4 pb-3">
          <div className="min-w-0">
            {title && <h2 id={titleId} className="text-sm font-semibold text-[var(--fg)]">{title}</h2>}
            {description && <p id={descId} className="mt-1 text-xs leading-relaxed text-[var(--fg-muted)]">{description}</p>}
          </div>
          <button type="button" onClick={onClose} className="-mr-1.5 -mt-0.5 rounded-md p-1 text-[var(--fg-subtle)] transition-colors hover:bg-[var(--surface-2)] hover:text-[var(--fg)]" aria-label="Close"><X className="h-4 w-4" /></button>
        </div>
        <div className="px-5 pb-4">{children}</div>
        {footer && <div className="flex justify-end gap-2 rounded-b-[var(--radius-lg)] border-t border-[var(--border-subtle)] bg-[var(--surface-1)] px-5 py-3">{footer}</div>}
      </div>
    </div>
  )
}
