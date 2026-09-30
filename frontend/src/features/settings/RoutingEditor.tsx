import { useMemo, useState } from 'react'
import { toast } from 'sonner'
import { Plus, Trash2 } from 'lucide-react'
import { Badge, Button, Drawer, Field, Input, Select, Textarea } from '@/components/ui'
import type { AgentProfile, MenuOption, ResolvedRoute, RoutingConfig, SIPNumber } from '@/lib/types'
import { cn, fmtPhone } from '@/lib/utils'
import { DEFAULT_SCHEDULE_DRAFT, TIMEZONES, WEEKDAYS, draftFromSchedule, scheduleToJSON, validateSchedule, type ScheduleDraft } from '../campaigns/schedule'
import { Toggle } from '../campaigns/editors'
import { ErrorNote, errMsg } from './common'
import { useResolveRoute, useSaveRouting } from './hooks'

export const MENU_KEYS = ['0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '*', '#']

export interface MenuRowDraft { key: string; label: string; agentProfileId: string }
export interface RoutingDraft {
  hours: ScheduleDraft
  afterHoursMode: 'profile' | 'message'
  afterHoursProfileId: string
  afterHoursMessage: string
  menuPrompt: string
  menu: MenuRowDraft[]
  menuTimeoutSec: number
  menuRepeat: number
}

export const DEFAULT_ROUTING_DRAFT: RoutingDraft = {
  hours: { ...DEFAULT_SCHEDULE_DRAFT, anytime: true, pacePerMinute: 0 },
  afterHoursMode: 'message', afterHoursProfileId: '', afterHoursMessage: '',
  menuPrompt: '', menu: [], menuTimeoutSec: 8, menuRepeat: 1,
}

export function draftFromRouting(r?: Partial<RoutingConfig> | null): RoutingDraft {
  if (!r) return { ...DEFAULT_ROUTING_DRAFT }
  const hours = draftFromSchedule(r.businessHours)
  return {
    hours: { ...hours, pacePerMinute: 0 },
    afterHoursMode: r.afterHoursProfileId ? 'profile' : 'message',
    afterHoursProfileId: r.afterHoursProfileId ?? '',
    afterHoursMessage: r.afterHoursMessage ?? '',
    menuPrompt: r.menuPrompt ?? '',
    menu: (r.menu ?? []).map((m) => ({ key: m.key, label: m.label, agentProfileId: m.agentProfileId })),
    menuTimeoutSec: r.menuTimeoutSec || DEFAULT_ROUTING_DRAFT.menuTimeoutSec,
    menuRepeat: r.menuRepeat ?? DEFAULT_ROUTING_DRAFT.menuRepeat,
  }
}

/** Draft → API JSON. 24/7 is sent as an empty window (no start/end). */
export function routingToJSON(d: RoutingDraft): RoutingConfig {
  const j = scheduleToJSON({ ...d.hours, pacePerMinute: 0 })
  return {
    businessHours: d.hours.anytime
      ? { timezone: d.hours.timezone, weekdays: [], startTime: '', endTime: '', pacePerMinute: 0 }
      : { timezone: j.timezone ?? d.hours.timezone, weekdays: j.weekdays ?? [], startTime: j.startTime ?? '', endTime: j.endTime ?? '', pacePerMinute: 0 },
    afterHoursProfileId: d.afterHoursMode === 'profile' && d.afterHoursProfileId ? d.afterHoursProfileId : null,
    afterHoursMessage: d.afterHoursMode === 'message' ? d.afterHoursMessage.trim() : '',
    menuPrompt: d.menu.length > 0 ? d.menuPrompt.trim() : '',
    menu: d.menu.map((m): MenuOption => ({ key: m.key, label: m.label.trim(), agentProfileId: m.agentProfileId })),
    menuTimeoutSec: d.menuTimeoutSec,
    menuRepeat: d.menuRepeat,
  }
}

export interface RoutingErrors { hours?: string; afterHours?: string; menuPrompt?: string; menu: { key?: string; label?: string; profile?: string }[]; timeout?: string; repeat?: string }

export function validateRouting(d: RoutingDraft): RoutingErrors {
  const e: RoutingErrors = { menu: [] }
  const hours = validateSchedule(d.hours)
  if (hours) e.hours = hours
  if (!d.hours.anytime && d.afterHoursMode === 'profile' && !d.afterHoursProfileId) e.afterHours = 'Профайл сонгоно уу'
  const counts = new Map<string, number>()
  for (const m of d.menu) counts.set(m.key, (counts.get(m.key) ?? 0) + 1)
  e.menu = d.menu.map((m) => ({
    key: (counts.get(m.key) ?? 0) > 1 ? 'Товч давхардсан' : undefined,
    label: m.label.trim() ? undefined : 'Нэр оруулна уу',
    profile: m.agentProfileId ? undefined : 'Профайл сонгоно уу',
  }))
  if (d.menu.length > 0 && !d.menuPrompt.trim()) e.menuPrompt = 'Цэсийн мэндчилгээ оруулна уу'
  if (!Number.isInteger(d.menuTimeoutSec) || d.menuTimeoutSec < 3 || d.menuTimeoutSec > 30) e.timeout = '3–30 секунд'
  if (!Number.isInteger(d.menuRepeat) || d.menuRepeat < 0 || d.menuRepeat > 3) e.repeat = '0–3'
  return e
}

export function routingValid(e: RoutingErrors): boolean {
  return !e.hours && !e.afterHours && !e.menuPrompt && !e.timeout && !e.repeat && e.menu.every((m) => !m.key && !m.label && !m.profile)
}

// ---------------------------------------------------------------- business hours

function BusinessHoursEditor({ value, onChange, error }: { value: ScheduleDraft; onChange: (v: ScheduleDraft) => void; error?: string }) {
  const set = (patch: Partial<ScheduleDraft>) => onChange({ ...value, ...patch })
  const tz = useMemo(() => (TIMEZONES.includes(value.timezone) ? TIMEZONES : [value.timezone, ...TIMEZONES]).map((t) => ({ value: t, label: t })), [value.timezone])
  const toggleDay = (d: number) => set({ weekdays: value.weekdays.includes(d) ? value.weekdays.filter((x) => x !== d) : [...value.weekdays, d] })
  return (
    <div className="space-y-4" data-testid="business-hours-editor">
      <div className="flex items-center justify-between gap-4 rounded-md border border-[var(--border)] bg-[var(--surface-0)] px-3 py-2">
        <div>
          <div className="text-sm text-[var(--fg)]">24/7</div>
          <div className="text-xs text-[var(--fg-subtle)]">Цагийн хязгааргүй, ажлын бус цаг гэж байхгүй</div>
        </div>
        <Toggle checked={value.anytime} label="24/7" onChange={(anytime) => set(anytime ? { anytime } : { anytime, weekdays: value.weekdays.length ? value.weekdays : DEFAULT_SCHEDULE_DRAFT.weekdays, startTime: value.startTime || DEFAULT_SCHEDULE_DRAFT.startTime, endTime: value.endTime || DEFAULT_SCHEDULE_DRAFT.endTime })} />
      </div>
      {!value.anytime && (
        <>
          <Field label="Цагийн бүс"><Select value={value.timezone} onChange={(e) => set({ timezone: e.target.value })} options={tz} /></Field>
          <div>
            <div className="mb-1.5 text-xs font-medium text-[var(--fg-muted)]">Ажлын өдрүүд</div>
            <div className="flex flex-wrap gap-1.5" role="group" aria-label="Ажлын өдрүүд">
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
      {error && <div role="alert" className="text-xs text-[var(--danger)]">{error}</div>}
    </div>
  )
}

// ---------------------------------------------------------------- preview

const MODE_LABEL: Record<ResolvedRoute['mode'], string> = { direct: 'Шууд агент', after_hours: 'Ажлын бус цаг', menu: 'DTMF цэс' }

export function nowLocalInput(): string {
  const d = new Date()
  d.setMinutes(d.getMinutes() - d.getTimezoneOffset())
  return d.toISOString().slice(0, 16)
}

function RoutePreview({ numberId, profiles }: { numberId: string; profiles: AgentProfile[] }) {
  const resolve = useResolveRoute()
  const [at, setAt] = useState(nowLocalInput)
  const route = resolve.data
  const name = (id?: string | null) => profiles.find((p) => p.id === id)?.name ?? id ?? '—'
  const run = () => {
    const date = new Date(at)
    if (Number.isNaN(date.getTime())) return
    resolve.mutate({ id: numberId, at: date.toISOString() }, { onError: (e) => toast.error(errMsg(e)) })
  }
  return (
    <section className="space-y-3 rounded-md border border-[var(--border)] bg-[var(--surface-0)] p-3" aria-label="Урьдчилан харах" data-testid="route-preview">
      <div>
        <h3 className="text-xs font-semibold text-[var(--fg)]">Урьдчилан харах</h3>
        <p className="text-xs text-[var(--fg-subtle)]">Хадгалсан тохиргоогоор тухайн цагт ирсэн дуудлага хаашаа очихыг шалгана. Эхлээд хадгална уу.</p>
      </div>
      <div className="flex items-end gap-2">
        <Field label="Дуудлага ирэх цаг" className="flex-1"><Input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} /></Field>
        <Button type="button" variant="secondary" loading={resolve.isPending} onClick={run}>Шалгах</Button>
      </div>
      <ErrorNote error={resolve.error} />
      {route && (
        <dl className="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1.5 text-sm" data-testid="route-result">
          <dt className="text-[var(--fg-muted)]">Горим</dt>
          <dd><Badge tone={route.mode === 'after_hours' ? 'warning' : route.mode === 'menu' ? 'info' : 'success'} data-mode={route.mode}>{MODE_LABEL[route.mode]}</Badge></dd>
          {route.agentProfileId && <><dt className="text-[var(--fg-muted)]">Профайл</dt><dd>{name(route.agentProfileId)}</dd></>}
          {route.message && <><dt className="text-[var(--fg-muted)]">Мессеж</dt><dd className="whitespace-pre-wrap">{route.message}</dd></>}
          {route.menuPrompt && <><dt className="text-[var(--fg-muted)]">Цэсийн текст</dt><dd className="whitespace-pre-wrap">{route.menuPrompt}</dd></>}
          {route.menu && route.menu.length > 0 && (
            <><dt className="text-[var(--fg-muted)]">Цэс</dt>
              <dd><ul>{route.menu.map((m) => <li key={m.key}><span className="font-mono">{m.key}</span> — {m.label} <span className="text-[var(--fg-subtle)]">({name(m.agentProfileId)})</span></li>)}</ul></dd></>
          )}
        </dl>
      )}
    </section>
  )
}

// ---------------------------------------------------------------- editor

export function RoutingEditor({ open, onClose, number, profiles }: { open: boolean; onClose: () => void; number: SIPNumber | null; profiles: AgentProfile[] }) {
  return (
    <Drawer open={open} onClose={onClose} title={number ? `Чиглүүлэлт: ${fmtPhone(number.number)}` : 'Чиглүүлэлт'} width="max-w-xl">
      {open && number && <RoutingForm key={number.id} number={number} profiles={profiles} onClose={onClose} />}
    </Drawer>
  )
}

function RoutingForm({ number, profiles, onClose }: { number: SIPNumber; profiles: AgentProfile[]; onClose: () => void }) {
  const save = useSaveRouting()
  const [d, setD] = useState<RoutingDraft>(() => draftFromRouting(number.routing))
  const [touched, setTouched] = useState(false)
  const errors = validateRouting(d)
  const show = touched ? errors : ({ menu: [] } as RoutingErrors)
  const set = (patch: Partial<RoutingDraft>) => setD((s) => ({ ...s, ...patch }))
  const profileOptions = profiles.map((p) => ({ value: p.id, label: p.name }))

  const setRow = (i: number, patch: Partial<MenuRowDraft>) => set({ menu: d.menu.map((r, j) => (j === i ? { ...r, ...patch } : r)) })
  const addRow = () => {
    const key = MENU_KEYS.find((k) => !d.menu.some((m) => m.key === k))
    if (key) set({ menu: [...d.menu, { key, label: '', agentProfileId: '' }] })
  }

  function submit() {
    setTouched(true)
    if (!routingValid(errors)) return
    save.mutate({ id: number.id, routing: routingToJSON(d) }, {
      onSuccess: () => toast.success('Чиглүүлэлт хадгалагдлаа'),
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <div className="space-y-6 p-5">
      <section className="space-y-3">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--fg-subtle)]">Ажлын цаг</h3>
        <BusinessHoursEditor value={d.hours} onChange={(hours) => set({ hours })} error={show.hours} />
      </section>

      <section className="space-y-3">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--fg-subtle)]">Ажлын бус цагт</h3>
        {d.hours.anytime && <p className="text-xs text-[var(--fg-subtle)]">24/7 үед ажлын бус цаг байхгүй тул энэ тохиргоо хэрэглэгдэхгүй.</p>}
        <div role="radiogroup" aria-label="Ажлын бус цагийн горим" className="flex gap-4 text-sm">
          <label className="flex items-center gap-2"><input type="radio" name="ah-mode" className="accent-[var(--accent)]" checked={d.afterHoursMode === 'message'} onChange={() => set({ afterHoursMode: 'message' })} />Мессеж хэлээд таслах</label>
          <label className="flex items-center gap-2"><input type="radio" name="ah-mode" className="accent-[var(--accent)]" checked={d.afterHoursMode === 'profile'} onChange={() => set({ afterHoursMode: 'profile' })} />Өөр профайл</label>
        </div>
        {d.afterHoursMode === 'profile' ? (
          <Field label="Ажлын бус цагийн профайл" error={show.afterHours}>
            <Select value={d.afterHoursProfileId} onChange={(e) => set({ afterHoursProfileId: e.target.value })} placeholder="— Сонгох —" options={profileOptions} />
          </Field>
        ) : (
          <Field label="Ажлын бус цагийн мессеж" hint="Агент энийг хэлээд дуудлагыг таслана">
            <Textarea rows={3} value={d.afterHoursMessage} onChange={(e) => set({ afterHoursMessage: e.target.value })} placeholder="Манай ажлын цаг 09:00–18:00. Дараа дахин залгана уу." />
          </Field>
        )}
      </section>

      <section className="space-y-3">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--fg-subtle)]">DTMF цэс</h3>
        <p className="text-xs text-[var(--fg-subtle)]">Цэсийн мөр нэмбэл дуудлага ирэхэд агент цэсийг уншиж, дуудагч товч дарж профайл сонгоно.</p>
        {d.menu.length > 0 && (
          <Field label="Цэсийн мэндчилгээ" error={show.menuPrompt}>
            <Textarea rows={3} value={d.menuPrompt} onChange={(e) => set({ menuPrompt: e.target.value })} placeholder="Борлуулалтад 1, дэмжлэгт 2 дугаарыг дарна уу." />
          </Field>
        )}
        <ul className="space-y-2">
          {d.menu.map((r, i) => (
            <li key={i} data-testid="menu-row" className="grid grid-cols-[4.5rem_1fr_1fr_auto] items-start gap-2">
              <Field label="Товч" error={show.menu[i]?.key}>
                <Select aria-label={`Товч ${i + 1}`} value={r.key} onChange={(e) => setRow(i, { key: e.target.value })} options={MENU_KEYS.map((k) => ({ value: k, label: k }))} />
              </Field>
              <Field label="Нэр" error={show.menu[i]?.label}>
                <Input aria-label={`Цэсийн нэр ${i + 1}`} value={r.label} onChange={(e) => setRow(i, { label: e.target.value })} placeholder="Борлуулалт" />
              </Field>
              <Field label="Профайл" error={show.menu[i]?.profile}>
                <Select aria-label={`Цэсийн профайл ${i + 1}`} value={r.agentProfileId} onChange={(e) => setRow(i, { agentProfileId: e.target.value })} placeholder="— Сонгох —" options={profileOptions} />
              </Field>
              <Button type="button" variant="ghost" size="icon" className="mt-5" aria-label={`Цэсийн мөр ${i + 1} устгах`} onClick={() => set({ menu: d.menu.filter((_, j) => j !== i) })}><Trash2 className="h-3.5 w-3.5" /></Button>
            </li>
          ))}
        </ul>
        <Button type="button" variant="secondary" size="sm" disabled={d.menu.length >= MENU_KEYS.length} onClick={addRow}><Plus className="h-3.5 w-3.5" />Цэсийн мөр нэмэх</Button>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Товч хүлээх (сек)" hint="3–30" error={show.timeout}>
            <Input type="number" min={3} max={30} value={d.menuTimeoutSec} onChange={(e) => set({ menuTimeoutSec: Math.round(Number(e.target.value)) || 0 })} />
          </Field>
          <Field label="Давтах тоо" hint="0–3" error={show.repeat}>
            <Input type="number" min={0} max={3} value={d.menuRepeat} onChange={(e) => set({ menuRepeat: Math.round(Number(e.target.value)) || 0 })} />
          </Field>
        </div>
      </section>

      <RoutePreview numberId={number.id} profiles={profiles} />

      <ErrorNote error={save.error} />
      <div className="sticky bottom-0 -mx-5 flex justify-end gap-2 border-t border-[var(--border)] bg-[var(--surface-1)] px-5 py-3">
        <Button type="button" variant="ghost" onClick={onClose}>Хаах</Button>
        <Button type="button" loading={save.isPending} onClick={submit}>Хадгалах</Button>
      </div>
    </div>
  )
}
