import { describe, expect, it } from 'vitest'
import { detectColumns, parseCsvText, TEMPLATE_CSV } from './csv'

describe('csv preview', () => {
  it('detects the phone column (latin, cyrillic, BOM) and name column', () => {
    expect(detectColumns(['Утас', 'Нэр', 'note'])).toEqual({ phoneColumn: 'Утас', nameColumn: 'Нэр' })
    expect(detectColumns(['id', ' Mobile ', 'name'])).toEqual({ phoneColumn: ' Mobile ', nameColumn: 'name' })
    expect(detectColumns(['a', 'b'])).toEqual({ phoneColumn: null, nameColumn: null })
    const p = parseCsvText(TEMPLATE_CSV)
    expect(p.phoneColumn).toBe('phone')
    expect(p.nameColumn).toBe('name')
    expect(p.columns).toEqual(['phone', 'name', 'note'])
    expect(p.rowCount).toBe(2)
  })

  it('limits preview to 5 rows but counts all rows', () => {
    const csv = 'дугаар,нэр\n' + Array.from({ length: 12 }, (_, i) => `9911000${i},N${i}`).join('\n') + '\n\n'
    const p = parseCsvText(csv)
    expect(p.phoneColumn).toBe('дугаар')
    expect(p.rowCount).toBe(12)
    expect(p.previewRows).toHaveLength(5)
  })
})
