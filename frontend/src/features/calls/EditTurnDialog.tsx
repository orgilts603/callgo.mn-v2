import { useState } from 'react'
import { Button, Dialog, Field, Textarea } from '@/components/ui'
import type { TranscriptTurn } from '@/lib/types'
import { useTurnPatch } from './useTurnPatch'

export interface EditTurnDialogProps { open: boolean; onClose: () => void; callId: string; turn: TranscriptTurn }

/** Edit the full text of a transcript turn (PATCH with `{text}` only; no lexicon entry). */
export function EditTurnDialog({ open, onClose, callId, turn }: EditTurnDialogProps) {
  const [text, setText] = useState(turn.text)
  const mutation = useTurnPatch(callId, { successMessage: 'Хадгалагдлаа', onSuccess: onClose })
  const trimmed = text.trim()
  const valid = trimmed.length > 0 && trimmed !== turn.text

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Мөр засах"
      description="Зөвхөн энэ мөрийн текстийг засна. Lexicon-д нэмэгдэхгүй."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Болих</Button>
          <Button disabled={!valid} loading={mutation.isPending}
            onClick={() => mutation.mutate({ turnId: turn.id, body: { text: trimmed } })}>Хадгалах</Button>
        </>
      }
    >
      <Field label="Текст" hint={turn.rawText && turn.rawText !== turn.text ? `STT эх: ${turn.rawText}` : undefined}>
        <Textarea aria-label="Текст" autoFocus value={text} onChange={(e) => setText(e.target.value)} rows={4} />
      </Field>
    </Dialog>
  )
}
