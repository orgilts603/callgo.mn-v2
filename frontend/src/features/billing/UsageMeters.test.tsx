import { screen, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { installChartDom } from '@/app/testing'
import { UsageMeters, UsageMetersView } from './UsageMeters'
import { makeSubResponse, makeUsage, planBy } from './fixtures'
import { renderWith } from './testing'

vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))
const get = vi.mocked(api.get)

function minutesMeter() {
  return screen.getAllByTestId('usage-meter')[0]
}

describe('UsageMeters', () => {
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it.each([
    { minutes: 500, tone: 'success', bar: 'bg-[var(--success)]', width: '50%' },
    { minutes: 799, tone: 'success', bar: 'bg-[var(--success)]', width: '79.9%' },
    { minutes: 800, tone: 'warning', bar: 'bg-[var(--warning)]', width: '80%' },
    { minutes: 1000, tone: 'warning', bar: 'bg-[var(--warning)]', width: '100%' },
    { minutes: 1200, tone: 'danger', bar: 'bg-[var(--danger)]', width: '100%' },
  ])('colours the minutes meter $tone at $minutes / 1000', ({ minutes, tone, bar, width }) => {
    renderWith(<UsageMetersView usage={makeUsage({ minutes })} limits={planBy('starter')} />)
    const meter = minutesMeter()
    expect(meter).toHaveAttribute('data-tone', tone)
    const fill = within(meter).getByTestId('usage-meter-bar')
    expect(fill.className).toContain(bar)
    expect(fill.style.width).toBe(width)
  })

  it('shows overage minutes and MNT, and the secondary usage counters', () => {
    renderWith(<UsageMetersView usage={makeUsage({ minutes: 1200, overageMinutes: 200, overageMnt: 70_000 })} limits={planBy('starter')} />)
    expect(screen.getByText('Нэмэлт: 200 мин · 70 000 ₮')).toBeInTheDocument()
    expect(screen.getByText('1 234 567')).toBeInTheDocument() // LLM tokens
    expect(screen.getByText('500')).toBeInTheDocument() // STT minutes = 30 000 s / 60
    expect(screen.getByText('98 765')).toBeInTheDocument() // TTS chars
    expect(screen.getByText('120')).toBeInTheDocument() // calls
    expect(screen.getByText('12')).toBeInTheDocument() // SMS
  })

  it('treats 0 included minutes as unlimited', () => {
    const ent = planBy('enterprise')
    renderWith(<UsageMetersView usage={makeUsage({ includedMinutes: 0, minutes: 9000 })} limits={ent} />)
    expect(minutesMeter()).toHaveAttribute('data-tone', 'none')
    expect(within(minutesMeter()).queryByTestId('usage-meter-bar')).not.toBeInTheDocument()
  })

  it('loads the subscription and the daily usage for the current period', async () => {
    installChartDom()
    get.mockImplementation(async (path: string) => {
      if (path === '/billing/subscription') return makeSubResponse({ usage: { minutes: 900 } })
      if (path === '/billing/usage') return { summary: makeUsage(), daily: [{ day: '2026-09-01', minutes: 30, calls: 12, costMnt: 1000 }, { day: '2026-09-02', minutes: 42, calls: 20, costMnt: 1500 }] }
      throw new Error(`unexpected GET ${path}`)
    })
    renderWith(<UsageMeters />)
    expect(await screen.findByText('Өдөр тутмын хэрэглээ')).toBeInTheDocument()
    expect(minutesMeter()).toHaveAttribute('data-tone', 'warning')
    expect(get).toHaveBeenCalledWith('/billing/usage', { from: '2026-09-01T00:00:00Z', to: '2026-10-01T00:00:00Z' })
    expect(screen.getAllByText('2026-09-01 – 2026-10-01').length).toBeGreaterThan(0)
  })
})
