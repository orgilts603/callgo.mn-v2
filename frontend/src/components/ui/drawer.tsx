import { useEffect, useRef, type ReactNode } from 'react'
import { X } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface DrawerProps { open: boolean; onClose: () => void; title?: ReactNode; header?: ReactNode; children: ReactNode; width?: string; className?: string }
export function Drawer({ open, onClose, title, header, children, width = 'max-w-2xl', className }: DrawerProps) {
  const panel = useRef<HTMLElement>(null)
  useEffect(() => {
    if (!open) return
    const prev = document.activeElement as HTMLElement | null
    panel.current?.focus()
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => { window.removeEventListener('keydown', onKey); prev?.focus?.() }
  }, [open, onClose])
  return (
    <div className={cn('fixed inset-0 z-40', open ? 'pointer-events-auto' : 'pointer-events-none')} aria-hidden={!open} inert={!open}>
      <div className={cn('absolute inset-0 bg-[var(--backdrop)] transition-opacity duration-200', open ? 'opacity-100' : 'opacity-0')} onClick={onClose} />
      <aside ref={panel} tabIndex={-1} role="dialog" aria-modal={open || undefined}
        className={cn('absolute right-0 top-0 flex h-full w-full flex-col border-l border-[var(--border)] bg-[var(--surface-1)] shadow-[var(--shadow-lg)] transition-transform duration-200 ease-out focus:outline-none', width, open ? 'translate-x-0' : 'translate-x-full', className)}>
        <div className="flex h-[var(--topbar-h)] shrink-0 items-center justify-between gap-4 border-b border-[var(--border-subtle)] px-4">
          <div className="min-w-0 flex-1">{header ?? <h2 className="truncate text-[13px] font-semibold text-[var(--fg)]">{title}</h2>}</div>
          <button type="button" onClick={onClose} className="rounded-md p-1 text-[var(--fg-subtle)] transition-colors hover:bg-[var(--surface-2)] hover:text-[var(--fg)]" aria-label="Close"><X className="h-4 w-4" /></button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
      </aside>
    </div>
  )
}
