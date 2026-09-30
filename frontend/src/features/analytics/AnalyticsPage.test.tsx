import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, HttpError } from '@/lib/api'
import type { AnalyticsOverview, HeatmapCell } from '@/lib/types'
import { installChartDom, renderPage, resetAuth, signIn } from '@/app/testing'
import AnalyticsPage from './AnalyticsPage'
import { bucketFor, presetRange } from './hooks'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const overview: AnalyticsOverview = {
  calls: 1234, answered: 900, answerRate: 0.73, avgDurationSec: 125, totalMinutes: 2570.5, costMnt: 456000, costPerCallMnt: 370,
  sentiment: { positive: 500, neutral: 300, negative: 100 },
  outcomes: [{ code: 'interested', label: 'Сонирхсон', count: 120 }, { code: 'no_answer', label: 'Хариулаагүй', count: 40 }],
  byDirection: { inbound: 800, outbound: 434 },
}
const cells: HeatmapCell[] = [
  { weekday: 1, hour: 9, calls: 50, answerRate: 0.8 },
  { weekday: 1, hour: 10, calls: 10, answerRate: 0.5 },
  { weekday: 0, hour: 23, calls: 0, answerRate: 0 },
]
const points = Array.from({ length: 5 }, (_, i) => ({ ts: `2026-09-${String(20 + i).padStart(2, '0')}T00:00:00Z`, calls: i + 3, answered: i + 1, minutes: 10, costMnt: 100 }))

function mockApi() {
  return vi.spyOn(api, 'get').mockImplementation(async (path: string) => {
    switch (path) {
      case '/analytics/overview': return overview
      case '/analytics/timeseries': return { items: points }
      case '/analytics/heatmap': return { cells }
      case '/analytics/profiles': return { items: [{ profileId: 'p1', name: 'Борлуулалт', calls: 700, answerRate: 0.7, avgDurationSec: 95, positiveRate: 0.4, costMnt: 250000, outcomes: {} }] }
      case '/analytics/campaigns': return { items: [{ campaignId: 'k1', name: 'Намрын урамшуулал', total: 500, done: 400, failed: 50, skipped: 50, outcomes: {}, minutes: 812.4, costMnt: 120000 }] }
      default: throw new Error(`unexpected GET ${path}`)
    }
  })
}

describe('AnalyticsPage', () => {
  beforeEach(() => { resetAuth(); signIn(); installChartDom() })
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it('renders the overview cards, outcomes, tables and heatmap from the api', async () => {
    const get = mockApi()
    renderPage(<AnalyticsPage />)
    const stats = await screen.findByTestId('analytics-stats')
    await waitFor(() => expect(within(stats).getByText('1 234')).toBeInTheDocument())
    expect(within(stats).getByText('73%')).toBeInTheDocument()
    expect(within(stats).getByText('2:05')).toBeInTheDocument()
    expect(within(stats).getByText('2 570.5')).toBeInTheDocument()
    expect(within(stats).getByText('456 000 ₮')).toBeInTheDocument()
    expect(within(stats).getByText('370 ₮')).toBeInTheDocument()

    expect(await screen.findByTestId('sentiment-donut')).toBeInTheDocument()
    expect(within(await screen.findByTestId('outcomes')).getByText('Сонирхсон')).toBeInTheDocument()
    expect(await screen.findByText('Борлуулалт')).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: 'Намрын урамшуулал' })).toHaveAttribute('href', '/campaigns/k1')

    const grid = await screen.findByTestId('heatmap')
    const all = within(grid).getAllByTestId('heat-cell')
    expect(all).toHaveLength(7 * 24)
    const busy = within(grid).getByLabelText('Даваа 09:00 · 50 дуудлага · 80% хариулсан')
    expect(busy).toHaveAttribute('data-calls', '50')
    fireEvent.mouseEnter(busy)
    expect(screen.getByTestId('heatmap-tip')).toHaveTextContent('80% хариулсан')
    // busier cell is shaded more strongly than a quiet one
    const strong = /(\d+(?:\.\d+)?)%/.exec(busy.style.background)
    const weak = /(\d+(?:\.\d+)?)%/.exec(within(grid).getByLabelText(/Даваа 10:00/).style.background)
    if (strong && weak) expect(Number(strong[1])).toBeGreaterThan(Number(weak[1]))

    // default range is the last 30 days, day buckets
    const call = get.mock.calls.find(([p]) => p === '/analytics/timeseries')!
    expect(call[1]).toMatchObject({ bucket: 'day' })
    expect(typeof (call[1] as { from: string }).from).toBe('string')
  })

  it('switches presets and custom ranges, using hourly buckets for up to two days', async () => {
    const get = mockApi()
    renderPage(<AnalyticsPage />)
    await screen.findByTestId('analytics-stats')
    fireEvent.click(screen.getByRole('button', { name: '7 хоног' }))
    await waitFor(() => {
      const last = get.mock.calls.filter(([p]) => p === '/analytics/overview').at(-1)!
      const q = last[1] as { from: string; to: string }
      expect(Math.round((Date.parse(q.to) - Date.parse(q.from)) / 86_400_000)).toBe(7)
    })
    fireEvent.click(screen.getByRole('button', { name: 'Сонгох' }))
    fireEvent.change(screen.getByLabelText('Эхлэх огноо'), { target: { value: '2026-09-29' } })
    fireEvent.change(screen.getByLabelText('Дуусах огноо'), { target: { value: '2026-09-30' } })
    await waitFor(() => {
      const last = get.mock.calls.filter(([p]) => p === '/analytics/timeseries').at(-1)!
      expect(last[1]).toMatchObject({ bucket: 'hour' })
    })
  })

  it('bucketFor / presetRange helpers', () => {
    expect(bucketFor({ from: '2026-09-29', to: '2026-09-30' })).toBe('hour')
    expect(bucketFor({ from: '2026-09-28', to: '2026-09-30' })).toBe('day')
    expect(presetRange(7, new Date(2026, 8, 30))).toEqual({ from: '2026-09-24', to: '2026-09-30' })
  })

  it('shows the upgrade card on feature_unavailable', async () => {
    vi.spyOn(api, 'get').mockRejectedValue(new HttpError(403, 'feature_unavailable', 'no analytics'))
    renderPage(<AnalyticsPage />)
    const card = await screen.findByTestId('analytics-upgrade')
    expect(within(card).getByRole('link', { name: 'Багц сонгох' })).toHaveAttribute('href', '/settings/billing')
    expect(screen.queryByTestId('analytics-stats')).toBeNull()
  })

  it('downloads the CSV with the bearer token', async () => {
    mockApi()
    const fetchMock = vi.fn(async () => new Response('id,outcome\nc1,ok', { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    const createUrl = vi.fn(() => 'blob:csv')
    const revoke = vi.fn()
    Object.assign(URL, { createObjectURL: createUrl, revokeObjectURL: revoke })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderPage(<AnalyticsPage />)
    await screen.findByTestId('analytics-stats')
    fireEvent.click(screen.getByRole('button', { name: /CSV татах/ }))
    await waitFor(() => expect(click).toHaveBeenCalled())
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, { headers: Record<string, string> }]
    expect(url).toMatch(/^\/api\/analytics\/export\.csv\?from=.+&to=.+/)
    expect(init.headers.Authorization).toBe('Bearer test-token')
    expect(createUrl).toHaveBeenCalledTimes(1)
    expect(revoke).toHaveBeenCalledWith('blob:csv')
  })
})
