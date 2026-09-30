import { describe, expect, it } from 'vitest'
import { DEFAULT_OUTCOMES } from '@/lib/types'
import {
  ANYTIME_DRAFT, DEFAULT_SCHEDULE_DRAFT, defaultOutcomesDraft, draftFromOutcomes, draftFromSchedule, outcomesToJSON, outcomesValid,
  outcomeTone, scheduleChip, scheduleToJSON, validateOutcomes, validateSchedule, weekdaysLabel,
} from './schedule'

describe('schedule JSON', () => {
  it('24/7 clears the schedule', () => {
    expect(scheduleToJSON(ANYTIME_DRAFT)).toEqual({})
    expect(scheduleToJSON({ ...ANYTIME_DRAFT, pacePerMinute: 4 })).toEqual({ pacePerMinute: 4 })
  })
  it('builds a window with Monday-first sorted weekdays', () => {
    expect(scheduleToJSON({ ...DEFAULT_SCHEDULE_DRAFT, weekdays: [0, 6, 1], pacePerMinute: 3.4 })).toEqual({
      timezone: 'Asia/Ulaanbaatar', weekdays: [1, 6, 0], startTime: '09:00', endTime: '18:00', pacePerMinute: 3,
    })
  })
  it('round-trips through draftFromSchedule and treats {} as 24/7', () => {
    const json = scheduleToJSON(DEFAULT_SCHEDULE_DRAFT)
    expect(scheduleToJSON(draftFromSchedule(json))).toEqual(json)
    expect(draftFromSchedule({}).anytime).toBe(true)
    expect(draftFromSchedule(undefined).anytime).toBe(true)
  })
  it('validates window fields', () => {
    expect(validateSchedule(DEFAULT_SCHEDULE_DRAFT)).toBeNull()
    expect(validateSchedule({ ...DEFAULT_SCHEDULE_DRAFT, weekdays: [] })).toMatch(/өдөр/)
    expect(validateSchedule({ ...DEFAULT_SCHEDULE_DRAFT, endTime: '' })).toMatch(/цаг/)
    expect(validateSchedule({ ...DEFAULT_SCHEDULE_DRAFT, endTime: '09:00' })).toMatch(/ижил/)
    expect(validateSchedule({ ...ANYTIME_DRAFT, weekdays: [] })).toBeNull()
  })
  it('renders chips', () => {
    expect(scheduleChip({ timezone: 'UTC', weekdays: [1, 2, 3, 4, 5], startTime: '09:00', endTime: '18:00', pacePerMinute: 0 })).toBe('Да–Ба 09:00–18:00')
    expect(scheduleChip({ weekdays: [1, 3, 0], startTime: '10:00', endTime: '12:00' })).toBe('Да, Лх, Ня 10:00–12:00')
    expect(scheduleChip({ weekdays: [1, 2, 3, 4, 5, 6, 0], startTime: '08:00', endTime: '20:00' })).toBe('Өдөр бүр 08:00–20:00')
    expect(scheduleChip({})).toBeNull()
    expect(scheduleChip(undefined)).toBeNull()
    expect(weekdaysLabel([6, 0])).toBe('Бя, Ня')
  })
})

describe('outcomes', () => {
  it('seeds from DEFAULT_OUTCOMES and serialises trimmed', () => {
    expect(outcomesToJSON(defaultOutcomesDraft())).toEqual(DEFAULT_OUTCOMES)
    expect(outcomesToJSON({ none: false, rows: [{ code: ' a ', label: ' A ', description: ' d ', terminal: false }] }))
      .toEqual([{ code: 'a', label: 'A', description: 'd', terminal: false }])
  })
  it('"Ангилалгүй" serialises to []', () => {
    expect(outcomesToJSON({ ...defaultOutcomesDraft(), none: true })).toEqual([])
    expect(draftFromOutcomes([]).none).toBe(true)
    expect(draftFromOutcomes(DEFAULT_OUTCOMES).none).toBe(false)
  })
  it('validates unique non-empty codes and labels', () => {
    const row = (code: string, label: string) => ({ code, label, description: '', terminal: true })
    expect(outcomesValid(defaultOutcomesDraft())).toBe(true)
    expect(validateOutcomes({ none: false, rows: [row('a', 'A'), row('A', 'B')] }).map((e) => e.code)).toEqual(['Код давхардсан', 'Код давхардсан'])
    expect(validateOutcomes({ none: false, rows: [row('', 'A'), row('b', '')] })).toEqual([{ code: 'Код оруулна уу' }, { label: 'Нэр оруулна уу' }])
    expect(outcomesValid({ none: false, rows: [] })).toBe(false)
    expect(outcomesValid({ none: true, rows: [] })).toBe(true)
  })
  it('picks tones', () => {
    expect(outcomeTone({ code: 'agreed', terminal: true })).toBe('success')
    expect(outcomeTone({ code: 'declined', terminal: true })).toBe('neutral')
    expect(outcomeTone({ code: 'callback', terminal: false })).toBe('warning')
  })
})
