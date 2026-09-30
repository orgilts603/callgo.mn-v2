import { useEffect, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface DrawerProps { open: boolean; onClose: () => void; title?: ReactNode; header?: ReactNode; children: ReactNode; width?: string; className?: string }
export function Drawer({ open, onClose, title, header, children, width = 'max-w-2xl', className }: DrawerProps) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])
  return (
    <div className={cn('fixed inset-0 z-40', open ? 'pointer-events-auto' : 'pointer-events-none')} aria-hidden={!open}>
      <div className={cn('absolute inset-0 bg-black/50 transition-opacity', open ? 'opacity-100' : 'opacity-0')} onClick={onClose} />
      <aside className={cn('absolute right-0 top-0 flex h-full w-full flex-col border-l border-[var(--border)] bg-[var(--surface-1)] shadow-2xl transition-transform duration-200', width, open ? 'translate-x-0' : 'translate-x-full', className)}>
        <div className="flex items-center justify-between gap-4 border-b border-[var(--border)] px-5 py-3">
          <div className="min-w-0 flex-1">{header ?? <h2 className="truncate text-sm font-semibold text-[var(--fg)]">{title}</h2>}</div>
          <button onClick={onClose} className="rounded-md p-1 text-[var(--fg-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)]" aria-label="Close"><X className="h-4 w-4" /></button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
      </aside>
    </div>
  )
}
