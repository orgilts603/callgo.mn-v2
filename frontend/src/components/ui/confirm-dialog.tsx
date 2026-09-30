import { useState, type ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Dialog } from './dialog'
import { Button } from './button'

export interface ConfirmDialogProps {
  open: boolean
  onClose: () => void
  /** May return a promise; the confirm button shows a spinner until it settles, then the dialog closes. */
  onConfirm: () => void | Promise<unknown>
  title: ReactNode
  description?: ReactNode
  children?: ReactNode
  confirmLabel?: ReactNode
  cancelLabel?: ReactNode
  /** `danger` (default) for destructive actions, `primary` otherwise. */
  tone?: 'danger' | 'primary'
  /** Externally controlled loading state (e.g. a mutation's isPending). */
  loading?: boolean
}

export function ConfirmDialog({
  open, onClose, onConfirm, title, description, children,
  confirmLabel = 'Баталгаажуулах', cancelLabel = 'Болих', tone = 'danger', loading,
}: ConfirmDialogProps) {
  const [busy, setBusy] = useState(false)
  const pending = busy || !!loading
  const confirm = async () => {
    setBusy(true)
    try {
      await onConfirm()
      onClose()
    } catch {
      /* the caller surfaces the error (toast); keep the dialog open */
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onClose={pending ? () => {} : onClose} className="max-w-md"
      title={
        <span className="flex items-center gap-2">
          {tone === 'danger' && <AlertTriangle className="h-4 w-4 text-[var(--danger)]" aria-hidden />}
          {title}
        </span>
      }
      description={description}
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={pending}>{cancelLabel}</Button>
          <Button variant={tone} size="sm" onClick={confirm} loading={pending} autoFocus>{confirmLabel}</Button>
        </>
      }>
      {children}
    </Dialog>
  )
}
