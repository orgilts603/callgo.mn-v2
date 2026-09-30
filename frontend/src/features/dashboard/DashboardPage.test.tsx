import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type { Call, CallStats, DailyCallCount, LiveEvent } from '@/lib/types'
import { installChartDom, renderPage, resetAuth, signIn, stubLive } from '@/app/testing'
import DashboardPage from './DashboardPage'

const stats: CallStats = {
  totalCalls: 1234, activeCalls: 3, completedToday: 42, avgDurationSec: 125,
  positiveRatio: 0.62, negativeRatio: 0.085, inboundToday: 30, outboundToday: 17,
}
const daily: DailyCallCount[] = Array.from({ length: 14 }, (_, i) => ({
  day: `2026-09-${String(17 + i).padStart(2, '0')}`, inbound: i + 2, outbound: i, completed: i + 1, failed: 1,
}))
const call = (id: string, over: Partial<Call> = {}): Call => ({
  id, orgId: 'o1', direction: 'inbound', status: 'completed', fromNumber: '+97699112233', toNumber: '+97677000000',
  roomName: `room-${id}`, startedAt: new Date(Date.now() - 5 * 60_000).toISOString(), durationSec: 95, sentiment: 'positive',
  createdAt: '2026-09-30T10:00:00Z', updatedAt: '2026-09-30T10:00:00Z', ...over,
})

function mockApi({ calls = [call('c1'), call('c2', { direction: 'outbound', status: 'failed', sentiment: 'negative', toNumber: '+97688001122' })], days = daily } = {}) {
  return vi.spyOn(api, 'get').mockImplementation(async (path: string) => {
    if (path === '/stats') return stats
    if (path === '/stats/daily') return { items: days }
    if (path === '/calls') return { items: calls, total: calls.length }
    throw new Error(`unexpected GET ${path}`)
  })
}

describe('DashboardPage', () => {
  beforeEach(() => { resetAuth(); signIn(); stubLive(); installChartDom() })
  afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it('renders stat cards from /api/stats', async () => {
    const get = mockApi()
    renderPage(<DashboardPage />)
    expect(await screen.findByText('1 234')).toBeInTheDocument()
    expect(screen.getByText('Нийт дуудлага')).toBeInTheDocument()
    expect(screen.getByText('42')).toBeInTheDocument()
    expect(screen.getByText('2:05')).toBeInTheDocument()
    expect(screen.getByText('62%')).toBeInTheDocument()
    expect(screen.getByText('8.5%')).toBeInTheDocument()
    expect(screen.getByText('Өнөөдөр 30 ирсэн · 17 гарсан')).toBeInTheDocument()
    expect(get).toHaveBeenCalledWith('/stats/daily', { days: 14 })
    expect(get).toHaveBeenCalledWith('/calls', { limit: 10 })
  })

  it('uses the live socket count for active calls while connected', async () => {
    mockApi()
    stubLive({ status: 'open', activeCalls: {} })
    renderPage(<DashboardPage />)
    const card = (await screen.findByText('Идэвхтэй', { selector: 'span' })).closest('div')!.parentElement!
    expect(within(card).getByText('0')).toBeInTheDocument()
  })

  it('lists recent calls linking to their detail page', async () => {
    mockApi()
    renderPage(<DashboardPage />)
    const link = await screen.findByRole('link', { name: /\+976 9911 2233/ })
    expect(link).toHaveAttribute('href', '/calls/c1')
    expect(screen.getByRole('link', { name: /\+976 8800 1122/ })).toHaveAttribute('href', '/calls/c2')
    expect(screen.getByText('Амжилтгүй')).toBeInTheDocument()
    expect(screen.getByText('Сөрөг')).toBeInTheDocument()
  })

  it('renders the daily chart legend with totals', async () => {
    mockApi()
    const { container } = renderPage(<DashboardPage />)
    await screen.findByText('Дуудлагын динамик')
    await waitFor(() => expect(container.querySelector('.recharts-wrapper')).not.toBeNull())
    const inboundTotal = daily.reduce((a, d) => a + d.inbound, 0)
    expect(screen.getByText(String(inboundTotal))).toBeInTheDocument()
  })

  it('shows empty states when there is no data', async () => {
    mockApi({ calls: [], days: [] })
    renderPage(<DashboardPage />)
    expect(await screen.findByText('Дуудлага алга')).toBeInTheDocument()
    expect(await screen.findByText('Мэдээлэл алга')).toBeInTheDocument()
  })

  it('refetches stats when a call.* event arrives', async () => {
    let emit: ((ev: LiveEvent) => void) | undefined
    stubLive({ onEvent: (fn) => { emit = fn; return () => {} } })
    const get = mockApi()
    renderPage(<DashboardPage />)
    await screen.findByText('1 234')
    const before = get.mock.calls.filter(([p]) => p === '/stats').length
    act(() => emit?.({ id: 'e1', type: 'call.ended', orgId: 'o1', at: new Date().toISOString(), payload: { call: call('c3') } }))
    await waitFor(() => expect(get.mock.calls.filter(([p]) => p === '/stats').length).toBeGreaterThan(before), { timeout: 3000 })
    expect(useLive.getState().status).toBe('closed')
  })
})
