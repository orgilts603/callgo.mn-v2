import { DEFAULT_OUTCOMES, type CampaignOutcome, type CampaignSchedule } from '@/lib/types'

/** Weekday toggles in Monday-first order. `value` follows the API: 0=Sunday..6=Saturday. */
export const WEEKDAYS: { value: number; label: string; full: string }[] = [
  { value: 1, label: 'Да', full: 'Даваа' },
  { value: 2, label: 'Мя', full: 'Мягмар' },
  { value: 3, label: 'Лх', full: 'Лхагва' },
  { value: 4, label: 'Пү', full: 'Пүрэв' },
  { value: 5, label: 'Ба', full: 'Баасан' },
  { value: 6, label: 'Бя', full: 'Бямба' },
  { value: 0, label: 'Ня', full: 'Ням' },
]
const ORDER = WEEKDAYS.map((d) => d.value)
const dayLabel = (v: number) => WEEKDAYS.find((d) => d.value === v)?.label ?? String(v)

export const TIMEZONES = [
  'Asia/Ulaanbaatar', 'Asia/Hovd', 'Asia/Choibalsan', 'UTC', 'Asia/Tokyo', 'Asia/Seoul', 'Asia/Shanghai', 'Europe/Moscow', 'Europe/London', 'America/New_York',
]

/** Editable form state of the calling window. `anytime` = 24/7 (no window). */
export interface ScheduleDraft {
  anytime: boolean
  timezone: string
  weekdays: number[]
  startTime: string
  endTime: string
  pacePerMinute: number
}

export const DEFAULT_SCHEDULE_DRAFT: ScheduleDraft = {
  anytime: false, timezone: 'Asia/Ulaanbaatar', weekdays: [1, 2, 3, 4, 5], startTime: '09:00', endTime: '18:00', pacePerMinute: 10,
}
/** Initial state of the new-campaign form: dial anytime, no pace limit. */
export const ANYTIME_DRAFT: ScheduleDraft = { ...DEFAULT_SCHEDULE_DRAFT, anytime: true, pacePerMinute: 0 }

/** Draft → API JSON. 24/7 clears the window (`{}`); only a pace limit may remain. */
export function scheduleToJSON(d: ScheduleDraft): Partial<CampaignSchedule> {
  const pace = Math.max(0, Math.round(d.pacePerMinute) || 0)
  if (d.anytime) return pace > 0 ? { pacePerMinute: pace } : {}
  return {
    timezone: d.timezone,
    weekdays: ORDER.filter((w) => d.weekdays.includes(w)),
    startTime: d.startTime,
    endTime: d.endTime,
    pacePerMinute: pace,
  }
}

export function hasWindow(s?: Partial<CampaignSchedule> | null): boolean {
  return !!s && !!s.startTime && !!s.endTime
}

export function draftFromSchedule(s?: Partial<CampaignSchedule> | null): ScheduleDraft {
  if (!s || !hasWindow(s)) return { ...ANYTIME_DRAFT, pacePerMinute: s?.pacePerMinute ?? 0 }
  return {
    anytime: false,
    timezone: s.timezone || DEFAULT_SCHEDULE_DRAFT.timezone,
    weekdays: s.weekdays ?? [],
    startTime: s.startTime ?? '',
    endTime: s.endTime ?? '',
    pacePerMinute: s.pacePerMinute ?? 0,
  }
}

export function validateSchedule(d: ScheduleDraft): string | null {
  if (d.anytime) return null
  if (d.weekdays.length === 0) return 'Дор хаяж нэг өдөр сонгоно уу'
  if (!d.startTime || !d.endTime) return 'Эхлэх болон дуусах цагийг оруулна уу'
  if (d.startTime === d.endTime) return 'Эхлэх ба дуусах цаг ижил байж болохгүй'
  return null
}

/** "Да–Ба" for a consecutive Monday-first run, else "Да, Лх, Ня". 7 days → "Өдөр бүр". */
export function weekdaysLabel(weekdays: number[]): string {
  const days = ORDER.filter((w) => weekdays.includes(w))
  if (days.length === 0) return ''
  if (days.length === 7) return 'Өдөр бүр'
  const idx = days.map((d) => ORDER.indexOf(d))
  const consecutive = idx.every((v, i) => i === 0 || v === idx[i - 1] + 1)
  if (consecutive && days.length > 2) return `${dayLabel(days[0])}–${dayLabel(days[days.length - 1])}`
  return days.map(dayLabel).join(', ')
}

/** Chip text like "Да–Ба 09:00–18:00"; null when no window is set. */
export function scheduleChip(s?: Partial<CampaignSchedule> | null): string | null {
  if (!s || !hasWindow(s)) return null
  const days = weekdaysLabel(s.weekdays ?? [])
  return `${days ? `${days} ` : ''}${s.startTime}–${s.endTime}`
}

// ---- outcomes ----

/** Editable outcome categories. `none` = "Ангилалгүй" (send `[]`). */
export interface OutcomesDraft { none: boolean; rows: CampaignOutcome[] }

export const defaultOutcomesDraft = (): OutcomesDraft => ({ none: false, rows: DEFAULT_OUTCOMES.map((o) => ({ ...o })) })

export function outcomesToJSON(d: OutcomesDraft): CampaignOutcome[] {
  if (d.none) return []
  return d.rows.map((r) => ({ code: r.code.trim(), label: r.label.trim(), description: r.description.trim(), terminal: r.terminal }))
}

export function draftFromOutcomes(o?: CampaignOutcome[] | null): OutcomesDraft {
  if (!o || o.length === 0) return { none: true, rows: defaultOutcomesDraft().rows }
  return { none: false, rows: o.map((r) => ({ ...r })) }
}

export interface OutcomeRowErrors { code?: string; label?: string }

/** Per-row validation: code and label required, code unique (case-insensitive). Empty array when valid. */
export function validateOutcomes(d: OutcomesDraft): OutcomeRowErrors[] {
  if (d.none) return []
  const counts = new Map<string, number>()
  for (const r of d.rows) { const k = r.code.trim().toLowerCase(); if (k) counts.set(k, (counts.get(k) ?? 0) + 1) }
  return d.rows.map((r) => {
    const e: OutcomeRowErrors = {}
    const code = r.code.trim().toLowerCase()
    if (!code) e.code = 'Код оруулна уу'
    else if ((counts.get(code) ?? 0) > 1) e.code = 'Код давхардсан'
    if (!r.label.trim()) e.label = 'Нэр оруулна уу'
    return e
  })
}

export function outcomesValid(d: OutcomesDraft): boolean {
  if (d.none) return true
  if (d.rows.length === 0) return false
  return validateOutcomes(d).every((e) => !e.code && !e.label)
}

export type OutcomeTone = 'success' | 'neutral' | 'warning'
/** Non-terminal (callback) → warning; agreed → success; other terminal → neutral. */
export function outcomeTone(o: Pick<CampaignOutcome, 'code' | 'terminal'>): OutcomeTone {
  if (!o.terminal) return 'warning'
  return o.code === 'agreed' ? 'success' : 'neutral'
}
