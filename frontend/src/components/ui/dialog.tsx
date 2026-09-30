import { useEffect, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface DialogProps { open: boolean; onClose: () => void; title?: ReactNode; description?: ReactNode; children: ReactNode; footer?: ReactNode; className?: string }
export function Dialog({ open, onClose, title, description, children, footer, className }: DialogProps) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4" role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div className={cn('relative w-full max-w-lg rounded-xl border border-[var(--border)] bg-[var(--surface-1)] shadow-2xl', className)}>
        <div className="flex items-start justify-between gap-4 border-b border-[var(--border)] px-5 py-4">
          <div>
            {title && <h2 className="text-base font-semibold text-[var(--fg)]">{title}</h2>}
            {description && <p className="mt-0.5 text-xs text-[var(--fg-muted)]">{description}</p>}
          </div>
          <button onClick={onClose} className="rounded-md p-1 text-[var(--fg-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)]" aria-label="Close"><X className="h-4 w-4" /></button>
        </div>
        <div className="px-5 py-4">{children}</div>
        {footer && <div className="flex justify-end gap-2 border-t border-[var(--border)] px-5 py-3">{footer}</div>}
      </div>
    </div>
  )
}
