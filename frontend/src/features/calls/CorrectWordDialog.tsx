import { useMemo, useState, type FormEvent } from 'react'
import { ArrowRight } from 'lucide-react'
import { Button, Dialog, Field, Input, Select, type SelectOption } from '@/components/ui'
import type { LexiconScope, TranscriptTurn } from '@/lib/types'
import { buildCorrectionBody, wordAt } from './transcriptText'
import { useTurnPatch } from './useTurnPatch'

export const SCOPE_OPTIONS: SelectOption[] = [
  { value: 'stt', label: 'STT — яриа таних' },
  { value: 'tts', label: 'TTS — дуудлага' },
  { value: 'both', label: 'Хоёулаа' },
]

export interface CorrectWordDialogProps {
  open: boolean
  onClose: () => void
  callId: string
  turn: TranscriptTurn
  /** Index of the clicked word among the turn's words. */
  wordIndex: number
}

/** Fix a single mis-recognised word; the backend stores it as a lexicon correction. */
export function CorrectWordDialog({ open, onClose, callId, turn, wordIndex }: CorrectWordDialogProps) {
  const wrong = useMemo(() => wordAt(turn.text, wordIndex), [turn.text, wordIndex])
  const [correct, setCorrect] = useState(wrong)
  const [scope, setScope] = useState<LexiconScope>(turn.speaker === 'agent' ? 'tts' : 'stt')
  const [phonetic, setPhonetic] = useState('')
  const mutation = useTurnPatch(callId, { successMessage: 'Lexicon-д нэмэгдлээ', onSuccess: onClose })

  const trimmed = correct.trim()
  const valid = trimmed.length > 0 && trimmed !== wrong
  const preview = valid ? buildCorrectionBody(turn, wordIndex, { correct: trimmed, scope }).text : turn.text

  const submit = (e?: FormEvent) => {
    e?.preventDefault()
    if (!valid || mutation.isPending) return
    mutation.mutate({ turnId: turn.id, body: buildCorrectionBody(turn, wordIndex, { correct: trimmed, scope, phonetic }) })
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Үг засах"
      description="Засвар нь lexicon-д хадгалагдаж, дараагийн дуудлагуудад автоматаар хэрэглэгдэнэ."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>Болих</Button>
          <Button onClick={() => submit()} disabled={!valid} loading={mutation.isPending}>Хадгалах</Button>
        </>
      }
    >
      <form className="space-y-4" onSubmit={submit}>
        <div className="flex items-center gap-3 rounded-lg border border-[var(--border)] bg-[var(--surface-inset)] px-3 py-2.5">
          <span className="text-xs text-[var(--fg-muted)]">Буруу</span>
          <span data-testid="wrong-word" className="rounded bg-[var(--danger-soft)] px-2 py-0.5 font-medium text-[var(--danger-fg)] line-through">{wrong}</span>
          <ArrowRight className="h-4 w-4 text-[var(--fg-subtle)]" />
          <span className="rounded bg-[var(--success-soft)] px-2 py-0.5 font-medium text-[var(--success-fg)]">{trimmed || '…'}</span>
        </div>
        <Field label="Зөв бичлэг">
          <Input autoFocus value={correct} onChange={(e) => setCorrect(e.target.value)} placeholder="Зөв үг" aria-label="Зөв бичлэг" />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Хамрах хүрээ">
            <Select aria-label="Хамрах хүрээ" value={scope} onChange={(e) => setScope(e.target.value as LexiconScope)} options={SCOPE_OPTIONS} />
          </Field>
          <Field label="Фонетик (заавал биш)" hint="TTS-д унших хэлбэр">
            <Input aria-label="Фонетик" value={phonetic} onChange={(e) => setPhonetic(e.target.value)} placeholder="ж: кол гоу" />
          </Field>
        </div>
        <div className="rounded-lg bg-[var(--surface-2)]/60 px-3 py-2 text-xs leading-relaxed text-[var(--fg-muted)]">
          <span className="mr-1 text-[var(--fg-subtle)]">Урьдчилан харах:</span>{preview}
        </div>
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}
