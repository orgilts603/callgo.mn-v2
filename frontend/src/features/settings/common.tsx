import type { ReactNode } from 'react'
import { Button, Dialog } from '@/components/ui'
import { cn } from '@/lib/utils'

export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : 'Алдаа гарлаа'
}

export function Switch({ checked, onChange, label, disabled, id }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean; id?: string }) {
  return (
    <button
      type="button" role="switch" id={id} aria-checked={checked} aria-label={label} disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-[var(--border)] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] disabled:opacity-50',
        checked ? 'bg-[var(--accent)]' : 'bg-[var(--surface-3)]',
      )}
    >
      <span className={cn('inline-block h-3.5 w-3.5 rounded-full bg-white shadow transition-transform', checked ? 'translate-x-[18px]' : 'translate-x-0.5')} />
    </button>
  )
}

export function SwitchRow({ label, hint, checked, onChange }: { label: string; hint?: ReactNode; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-md border border-[var(--border)] bg-[var(--surface-0)] px-3 py-2">
      <div>
        <div className="text-sm text-[var(--fg)]">{label}</div>
        {hint && <div className="text-xs text-[var(--fg-subtle)]">{hint}</div>}
      </div>
      <Switch checked={checked} onChange={onChange} label={label} />
    </div>
  )
}

export function ConfirmDialog({ open, title, description, confirmLabel = 'Устгах', loading, onConfirm, onClose }: {
  open: boolean; title: string; description?: ReactNode; confirmLabel?: string; loading?: boolean; onConfirm: () => void; onClose: () => void
}) {
  return (
    <Dialog
      open={open} onClose={onClose} title={title} description={description} className="max-w-md"
      footer={<>
        <Button variant="ghost" onClick={onClose}>Болих</Button>
        <Button variant="danger" loading={loading} onClick={onConfirm}>{confirmLabel}</Button>
      </>}
    >
      <p className="text-sm text-[var(--fg-muted)]">Энэ үйлдлийг буцаах боломжгүй.</p>
    </Dialog>
  )
}

export function ErrorNote({ error }: { error: unknown }) {
  if (!error) return null
  return <div role="alert" className="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">{errMsg(error)}</div>
}
