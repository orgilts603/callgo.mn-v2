import { daysUntil, fmtCountdown, fmtMnt, fmtNum, meterTone, planBullets, qrImageSrc } from './format'
import { planBy } from './fixtures'

describe('billing format', () => {
  it('formats MNT with space thousands separators', () => {
    expect(fmtMnt(1_290_000)).toBe('1 290 000 ₮')
    expect(fmtMnt(350)).toBe('350 ₮')
    expect(fmtMnt(0)).toBe('0 ₮')
    expect(fmtMnt(-12_000)).toBe('-12 000 ₮')
    expect(fmtNum(1234.56, 1)).toBe('1 234.6')
    expect(fmtNum(1000.04, 1)).toBe('1 000')
  })

  it('colours meters emerald < 80 %, amber 80–100 %, rose > 100 %', () => {
    expect(meterTone(0)).toBe('success')
    expect(meterTone(79.9)).toBe('success')
    expect(meterTone(80)).toBe('warning')
    expect(meterTone(100)).toBe('warning')
    expect(meterTone(100.1)).toBe('danger')
    expect(meterTone(250)).toBe('danger')
  })

  it('computes days left and countdowns', () => {
    const now = Date.parse('2026-09-30T12:00:00Z')
    expect(daysUntil('2026-10-02T12:00:00Z', now)).toBe(2)
    expect(daysUntil('2026-10-02T13:00:00Z', now)).toBe(3)
    expect(daysUntil('2026-09-01T00:00:00Z', now)).toBe(0)
    expect(daysUntil(null, now)).toBeNull()
    expect(fmtCountdown(125_000)).toBe('2:05')
    expect(fmtCountdown(-5)).toBe('0:00')
  })

  it('builds plan bullets with 0 = unlimited and blocked overage', () => {
    expect(planBullets(planBy('starter'))).toEqual(expect.arrayContaining(['1 000 минут багтсан', 'Нэмэлт минут: 350 ₮/мин', 'Дуудлагын бичлэг']))
    expect(planBullets(planBy('trial'))).toContain('Нэмэлт минут: боломжгүй')
    expect(planBullets(planBy('enterprise'))).toEqual(expect.arrayContaining(['Минут: тохиролцоно', 'Хязгааргүй агент профайл']))
  })

  it('accepts raw base64 or URL provider QR images', () => {
    expect(qrImageSrc('iVBORw0KGgo=')).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(qrImageSrc('data:image/png;base64,xx')).toBe('data:image/png;base64,xx')
    expect(qrImageSrc('https://x/qr.png')).toBe('https://x/qr.png')
  })
})
