import { useMemo } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { Button, Field, Input, Select } from '@/components/ui'
import { cn } from '@/lib/utils'
import {
  TIMEZONES, WEEKDAYS, DEFAULT_SCHEDULE_DRAFT, validateOutcomes, validateSchedule,
  type OutcomesDraft, type ScheduleDraft,
} from './schedule'

export function Toggle({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: string; disabled?: boolean }) {
  return (
    <button type="button" role="switch" aria-checked={checked} aria-label={label} disabled={disabled} onClick={() => onChange(!checked)}
      className={cn('relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-[var(--border)] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] disabled:cursor-not-allowed disabled:opacity-50',
        checked ? 'bg-[var(--accent)]' : 'bg-[var(--surface-3)]')}>
      <span className={cn('inline-block h-3.5 w-3.5 rounded-full bg-white shadow transition-transform', checked ? 'translate-x-[18px]' : 'translate-x-0.5')} />
    </button>
  )
}

function ToggleRow({ label, hint, checked, onChange }: { label: string; hint?: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-md border border-[var(--border)] bg-[var(--surface-0)] px-3 py-2">
      <div>
        <div className="text-sm text-[var(--fg)]">{label}</div>
        {hint && <div className="text-xs text-[var(--fg-subtle)]">{hint}</div>}
      </div>
      <Toggle checked={checked} onChange={onChange} label={label} />
    </div>
  )
}

// ---------------------------------------------------------------- schedule

export function ScheduleEditor({ value, onChange }: { value: ScheduleDraft; onChange: (v: ScheduleDraft) => void }) {
  const set = (patch: Partial<ScheduleDraft>) => onChange({ ...value, ...patch })
  const tzOptions = useMemo(
    () => (TIMEZONES.includes(value.timezone) ? TIMEZONES : [value.timezone, ...TIMEZONES]).map((t) => ({ value: t, label: t })),
    [value.timezone],
  )
  const error = validateSchedule(value)
  const toggleDay = (d: number) =>
    set({ weekdays: value.weekdays.includes(d) ? value.weekdays.filter((x) => x !== d) : [...value.weekdays, d] })

  return (
    <div className="space-y-4" data-testid="schedule-editor">
      <ToggleRow label="24/7" hint="Цагийн хязгааргүй, ямар ч үед залгана"
        checked={value.anytime}
        onChange={(anytime) => set(anytime ? { anytime } : { anytime, weekdays: value.weekdays.length ? value.weekdays : DEFAULT_SCHEDULE_DRAFT.weekdays, startTime: value.startTime || DEFAULT_SCHEDULE_DRAFT.startTime, endTime: value.endTime || DEFAULT_SCHEDULE_DRAFT.endTime })} />
      {!value.anytime && (
        <>
          <Field label="Цагийн бүс">
            <Select value={value.timezone} onChange={(e) => set({ timezone: e.target.value })} options={tzOptions} />
          </Field>
          <div>
            <div className="mb-1.5 text-xs font-medium text-[var(--fg-muted)]">Залгах өдрүүд</div>
            <div className="flex flex-wrap gap-1.5" role="group" aria-label="Залгах өдрүүд">
              {WEEKDAYS.map((d) => {
                const on = value.weekdays.includes(d.value)
                return (
                  <button key={d.value} type="button" aria-pressed={on} title={d.full} onClick={() => toggleDay(d.value)}
                    className={cn('h-8 w-10 rounded-md border text-xs font-medium transition-colors',
                      on ? 'border-[var(--accent)] bg-[var(--accent)] text-[var(--fg-on-accent)]' : 'border-[var(--border)] text-[var(--fg-muted)] hover:bg-[var(--surface-2)]')}>
                    {d.label}
                  </button>
                )
              })}
            </div>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Эхлэх цаг"><Input type="time" value={value.startTime} onChange={(e) => set({ startTime: e.target.value })} /></Field>
            <Field label="Дуусах цаг"><Input type="time" value={value.endTime} onChange={(e) => set({ endTime: e.target.value })} /></Field>
          </div>
        </>
      )}
      <Field label="Минутад залгах дугаар" hint="0 = хязгааргүй">
        <Input type="number" min={0} max={600} value={value.pacePerMinute}
          onChange={(e) => set({ pacePerMinute: Math.max(0, Math.round(Number(e.target.value)) || 0) })} />
      </Field>
      {error && <div role="alert" className="text-xs text-[var(--danger)]">{error}</div>}
    </div>
  )
}

// ---------------------------------------------------------------- outcomes

export function OutcomesEditor({ value, onChange }: { value: OutcomesDraft; onChange: (v: OutcomesDraft) => void }) {
  const errors = validateOutcomes(value)
  const setRow = (i: number, patch: Partial<OutcomesDraft['rows'][number]>) =>
    onChange({ ...value, rows: value.rows.map((r, j) => (j === i ? { ...r, ...patch } : r)) })
  const addRow = () => onChange({ ...value, rows: [...value.rows, { code: '', label: '', description: '', terminal: true }] })
  const removeRow = (i: number) => onChange({ ...value, rows: value.rows.filter((_, j) => j !== i) })

  return (
    <div className="space-y-3" data-testid="outcomes-editor">
      <ToggleRow label="Ангилалгүй" hint="Дуудлага бүрийн төгсгөлд AI үр дүн сонгохгүй" checked={value.none} onChange={(none) => onChange({ ...value, none })} />
      {!value.none && (
        <>
          <p className="text-xs text-[var(--fg-muted)]">
            Дуудлага бүрийн төгсгөлд AI доорх ангилалаас нэгийг сонгоно. «Төгсгөл» унтарсан ангилал (жишээ нь дахин залгах) бол дугаарыг дахин дараалалд оруулна.
          </p>
          <ul className="space-y-2">
            {value.rows.map((r, i) => (
              <li key={i} data-testid="outcome-row" className="space-y-2 rounded-md border border-[var(--border)] bg-[var(--surface-0)] p-3">
                <div className="grid gap-2 sm:grid-cols-[9rem_1fr_auto]">
                  <Field label="Код" error={errors[i]?.code}>
                    <Input value={r.code} onChange={(e) => setRow(i, { code: e.target.value })} placeholder="agreed" aria-invalid={!!errors[i]?.code} aria-label={`Код ${i + 1}`} />
                  </Field>
                  <Field label="Нэр" error={errors[i]?.label}>
                    <Input value={r.label} onChange={(e) => setRow(i, { label: e.target.value })} placeholder="Зөвшөөрсөн" aria-invalid={!!errors[i]?.label} aria-label={`Нэр ${i + 1}`} />
                  </Field>
                  <div className="flex items-end gap-2 pb-0.5">
                    <div className="flex h-8 items-center gap-2 text-xs text-[var(--fg-muted)]">
                      Төгсгөл <Toggle checked={r.terminal} onChange={(terminal) => setRow(i, { terminal })} label={`Төгсгөл ${i + 1}`} />
                    </div>
                    <Button type="button" variant="ghost" size="icon" aria-label={`Ангилал ${i + 1} устгах`} title="Устгах" onClick={() => removeRow(i)}>
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
                <Field label="Тайлбар (AI-д зориулсан)">
                  <Input value={r.description} onChange={(e) => setRow(i, { description: e.target.value })} aria-label={`Тайлбар ${i + 1}`} />
                </Field>
              </li>
            ))}
          </ul>
          {value.rows.length === 0 && <div role="alert" className="text-xs text-[var(--danger)]">Дор хаяж нэг ангилал нэмэх эсвэл «Ангилалгүй» сонгоно уу</div>}
          <Button type="button" variant="secondary" size="sm" onClick={addRow}><Plus className="h-3.5 w-3.5" />Ангилал нэмэх</Button>
        </>
      )}
    </div>
  )
}
