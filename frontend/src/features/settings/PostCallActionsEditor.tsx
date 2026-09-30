import { useRef, useState, type KeyboardEvent } from 'react'
import { Plus, Trash2, X } from 'lucide-react'
import { Button, Field, Input, Select, Textarea } from '@/components/ui'
import { DEFAULT_OUTCOMES, type PostCallAction, type PostCallActionType, type Webhook } from '@/lib/types'
import { useWebhooks } from './hooks'

export const PLACEHOLDERS = ['{{name}}', '{{phone}}', '{{summary}}', '{{outcome}}'] as const
export const MAX_CALLBACK_DELAY_MIN = 10080

const TYPE_OPTIONS: { value: PostCallActionType; label: string }[] = [
  { value: 'sms', label: 'SMS илгээх' },
  { value: 'webhook', label: 'Webhook дуудах' },
  { value: 'callback', label: 'Буцаж залгах' },
]

export const emptyAction = (type: PostCallActionType = 'sms'): PostCallAction => ({ type, outcomes: [], template: '', webhookId: null, delayMin: 0 })

export interface ActionErrors { template?: string; webhookId?: string; delayMin?: string }

export function validatePostCallActions(actions: PostCallAction[]): ActionErrors[] {
  return actions.map((a) => {
    const e: ActionErrors = {}
    if (a.type === 'sms' && !a.template.trim()) e.template = 'SMS загвар оруулна уу'
    if (a.type === 'webhook' && !a.webhookId) e.webhookId = 'Webhook сонгоно уу'
    if (a.type === 'callback' && (!Number.isInteger(a.delayMin) || a.delayMin < 0 || a.delayMin > MAX_CALLBACK_DELAY_MIN)) e.delayMin = `0–${MAX_CALLBACK_DELAY_MIN} минут`
    return e
  })
}
export const postCallActionsValid = (actions: PostCallAction[]) => validatePostCallActions(actions).every((e) => !e.template && !e.webhookId && !e.delayMin)

/** Drops the fields that do not belong to the action type so the API body is clean. */
export function normalizePostCallActions(actions: PostCallAction[]): PostCallAction[] {
  return actions.map((a) => ({
    type: a.type,
    outcomes: a.outcomes.map((o) => o.trim()).filter(Boolean),
    template: a.type === 'sms' ? a.template.trim() : '',
    webhookId: a.type === 'webhook' ? a.webhookId || null : null,
    delayMin: a.type === 'callback' ? a.delayMin : 0,
  }))
}

function ChipsInput({ value, onChange, label }: { value: string[]; onChange: (v: string[]) => void; label: string }) {
  const [text, setText] = useState('')
  const ref = useRef<HTMLInputElement>(null)
  const add = (raw: string) => {
    const items = raw.split(',').map((s) => s.trim()).filter((s) => s && !value.includes(s))
    if (items.length) onChange([...value, ...items])
    setText('')
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); add(text) }
    else if (e.key === 'Backspace' && !text && value.length) onChange(value.slice(0, -1))
  }
  const suggestions = DEFAULT_OUTCOMES.map((o) => o.code).filter((c) => !value.includes(c))
  return (
    <div className="space-y-1.5">
      <div className="flex min-h-8 flex-wrap items-center gap-1.5 rounded-[var(--radius-sm)] border border-[var(--border)] bg-[var(--surface-inset)] px-2 py-1 focus-within:border-[var(--accent)]" onClick={() => ref.current?.focus()}>
        {value.map((c) => (
          <span key={c} data-testid="outcome-chip" className="inline-flex items-center gap-1 rounded-full border border-[var(--border)] bg-[var(--surface-2)] px-2 py-0.5 font-mono text-[11px]">
            {c}
            <button type="button" aria-label={`${c} хасах`} onClick={(e) => { e.stopPropagation(); onChange(value.filter((x) => x !== c)) }} className="text-[var(--fg-subtle)] hover:text-[var(--fg)]"><X className="h-3 w-3" /></button>
          </span>
        ))}
        <input ref={ref} aria-label={label} value={text} onChange={(e) => setText(e.target.value)} onKeyDown={onKey} onBlur={() => add(text)}
          placeholder={value.length ? '' : 'agreed, callback …'} className="min-w-24 flex-1 bg-transparent text-[13px] outline-none placeholder:text-[var(--fg-subtle)]" />
      </div>
      {suggestions.length > 0 && (
        <div className="flex flex-wrap gap-1">
          {suggestions.map((c) => (
            <button key={c} type="button" onClick={() => onChange([...value, c])} className="rounded-full border border-dashed border-[var(--border)] px-2 py-0.5 font-mono text-[11px] text-[var(--fg-muted)] hover:bg-[var(--surface-2)]">+ {c}</button>
          ))}
        </div>
      )}
    </div>
  )
}

export function PostCallActionsEditor({ value, onChange, showErrors = false, webhooks: webhooksProp }: {
  value: PostCallAction[]; onChange: (v: PostCallAction[]) => void; showErrors?: boolean; webhooks?: Webhook[]
}) {
  const hooks = useWebhooks()
  const webhooks = webhooksProp ?? hooks.data ?? []
  const errors = showErrors ? validatePostCallActions(value) : []
  const setRow = (i: number, patch: Partial<PostCallAction>) => onChange(value.map((a, j) => (j === i ? { ...a, ...patch } : a)))

  return (
    <fieldset className="space-y-3" data-testid="post-call-actions">
      <legend className="text-xs font-medium text-[var(--fg-muted)]">Дуудлагын дараах үйлдлүүд</legend>
      <p className="text-xs text-[var(--fg-subtle)]">Дуудлага дууссаны дараа үр дүнгээс хамааран автоматаар ажиллана. Үр дүнг хоосон орхивол бүх дуудлагад ажиллана.</p>
      <ul className="space-y-3">
        {value.map((a, i) => (
          <li key={i} data-testid="post-call-action" className="space-y-3 rounded-md border border-[var(--border)] bg-[var(--surface-0)] p-3">
            <div className="grid gap-3 sm:grid-cols-[11rem_1fr_auto] sm:items-end">
              <Field label="Төрөл">
                <Select aria-label={`Үйлдлийн төрөл ${i + 1}`} value={a.type} onChange={(e) => setRow(i, { type: e.target.value as PostCallActionType })} options={TYPE_OPTIONS} />
              </Field>
              <div className="space-y-1.5">
                <span className="block text-xs font-medium text-[var(--fg-muted)]">Үр дүнгийн код</span>
                <ChipsInput value={a.outcomes} onChange={(outcomes) => setRow(i, { outcomes })} label={`Үр дүнгийн код ${i + 1}`} />
              </div>
              <Button type="button" variant="ghost" size="icon" aria-label={`Үйлдэл ${i + 1} устгах`} onClick={() => onChange(value.filter((_, j) => j !== i))}><Trash2 className="h-3.5 w-3.5" /></Button>
            </div>

            {a.type === 'sms' && (
              <div className="space-y-1.5">
                <Field label="SMS загвар" error={errors[i]?.template}>
                  <Textarea rows={3} aria-label={`SMS загвар ${i + 1}`} value={a.template} onChange={(e) => setRow(i, { template: e.target.value })} placeholder="Сайн байна уу {{name}}, ..." />
                </Field>
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-xs text-[var(--fg-subtle)]">Оруулах:</span>
                  {PLACEHOLDERS.map((p) => (
                    <button key={p} type="button" onClick={() => setRow(i, { template: `${a.template}${p}` })}
                      className="rounded-full border border-[var(--border)] bg-[var(--surface-2)] px-2 py-0.5 font-mono text-[11px] hover:bg-[var(--surface-3)]">{p}</button>
                  ))}
                </div>
              </div>
            )}
            {a.type === 'webhook' && (
              <Field label="Webhook" error={errors[i]?.webhookId} hint={webhooks.length === 0 ? 'Webhook байхгүй байна. Интеграц табаас үүсгэнэ үү.' : undefined}>
                <Select aria-label={`Webhook ${i + 1}`} value={a.webhookId ?? ''} onChange={(e) => setRow(i, { webhookId: e.target.value || null })} placeholder="— Сонгох —"
                  options={webhooks.map((w) => ({ value: w.id, label: w.description ? `${w.description} · ${w.url}` : w.url }))} />
              </Field>
            )}
            {a.type === 'callback' && (
              <Field label="Хоцрох хугацаа (минут)" error={errors[i]?.delayMin} hint="Дуудлага дууссанаас хойш хэдэн минутын дараа буцаж залгах">
                <Input type="number" min={0} max={MAX_CALLBACK_DELAY_MIN} aria-label={`Хоцрох хугацаа ${i + 1}`} value={a.delayMin}
                  onChange={(e) => setRow(i, { delayMin: Math.round(Number(e.target.value)) || 0 })} />
              </Field>
            )}
          </li>
        ))}
      </ul>
      <Button type="button" variant="secondary" size="sm" onClick={() => onChange([...value, emptyAction()])}><Plus className="h-3.5 w-3.5" />Үйлдэл нэмэх</Button>
    </fieldset>
  )
}
