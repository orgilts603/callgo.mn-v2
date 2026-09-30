import { useState, type FormEvent } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Button, ConfirmDialog, Dialog, Field, Input } from '@/components/ui'
import { fmtPhone } from '@/lib/utils'
import type { Call } from '@/lib/types'
import { activeCallsKey, callKey, callsApi } from './api'

export interface HangupDialogProps { call: Pick<Call, 'id' | 'fromNumber' | 'toNumber'> | null; onClose: () => void; onDone?: () => void }

/** Confirmation for POST /api/calls/{id}/hangup (shared ConfirmDialog). Renders nothing when `call` is null. */
export function HangupDialog({ call, onClose, onDone }: HangupDialogProps) {
  const qc = useQueryClient()
  const mutation = useMutation({
    mutationFn: (id: string) => callsApi.hangup(id),
    onSuccess: (_r, id) => {
      toast.success('Дуудлага тасаллаа')
      void qc.invalidateQueries({ queryKey: callKey(id) })
      void qc.invalidateQueries({ queryKey: activeCallsKey })
      onDone?.()
    },
    onError: (err: Error) => { toast.error(err.message || 'Дуудлага тасалж чадсангүй') },
  })
  if (!call) return null
  return (
    <ConfirmDialog open onClose={onClose} onConfirm={() => mutation.mutateAsync(call.id)} loading={mutation.isPending}
      title="Дуудлага таслах уу?" description={`${fmtPhone(call.fromNumber)} → ${fmtPhone(call.toNumber)}`} confirmLabel="Таслах">
      <p className="text-sm text-[var(--fg-muted)]">Яриа шууд тасарч, харилцагчтай холболт салгагдана.</p>
    </ConfirmDialog>
  )
}

const PHONE_RE = /^\+?\d{3,15}$/

export interface TransferDialogProps { callId: string | null; onClose: () => void; defaultNumber?: string }

/** Dialog for POST /api/calls/{id}/transfer. Renders nothing when `callId` is null. */
export function TransferDialog({ callId, onClose, defaultNumber = '' }: TransferDialogProps) {
  const qc = useQueryClient()
  const [number, setNumber] = useState(defaultNumber)
  const normalized = number.replace(/[\s()-]/g, '')
  const valid = PHONE_RE.test(normalized)
  const mutation = useMutation({
    mutationFn: ({ id, to }: { id: string; to: string }) => callsApi.transfer(id, to),
    onSuccess: (_r, v) => {
      toast.success(`${fmtPhone(v.to)} руу шилжүүллээ`)
      void qc.invalidateQueries({ queryKey: callKey(v.id) })
      onClose()
    },
    onError: (err: Error) => { toast.error(err.message || 'Шилжүүлж чадсангүй') },
  })
  if (!callId) return null
  const submit = (e?: FormEvent) => {
    e?.preventDefault()
    if (valid && !mutation.isPending) mutation.mutate({ id: callId, to: normalized })
  }
  return (
    <Dialog open onClose={onClose} title="Дуудлага шилжүүлэх" description="Яриаг оператор эсвэл өөр дугаар руу шилжүүлнэ."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Болих</Button>
          <Button disabled={!valid} loading={mutation.isPending} onClick={() => submit()}>Шилжүүлэх</Button>
        </>
      }>
      <form onSubmit={submit}>
        <Field label="Утасны дугаар" error={number && !valid ? 'Дугаар буруу байна' : undefined} hint="ж: +97699112233">
          <Input autoFocus inputMode="tel" value={number} onChange={(e) => setNumber(e.target.value)} placeholder="+976…" aria-label="Утасны дугаар" />
        </Field>
      </form>
    </Dialog>
  )
}
