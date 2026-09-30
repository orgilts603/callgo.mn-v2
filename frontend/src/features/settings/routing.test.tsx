import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// The suite shares a heavily loaded CI box; render-heavy forms need headroom.
vi.setConfig({ testTimeout: 30_000 })
import type { AgentProfile, RoutingConfig, SIPNumber } from '@/lib/types'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

import { api } from '@/lib/api'
import { RoutingEditor, draftFromRouting, routingToJSON, validateRouting } from './RoutingEditor'

const mocked = vi.mocked(api)

const profiles = [
  { id: 'p1', name: 'Борлуулалт' }, { id: 'p2', name: 'Дэмжлэг' },
] as AgentProfile[]

const emptyRouting: RoutingConfig = {
  businessHours: { timezone: 'Asia/Ulaanbaatar', weekdays: [], startTime: '', endTime: '', pacePerMinute: 0 },
  afterHoursProfileId: null, afterHoursMessage: '', menuPrompt: '', menu: [], menuTimeoutSec: 8, menuRepeat: 1,
}
const number = { id: 'n1', orgId: 'o', number: '+97670001234', label: '', allowInbound: true, allowOutbound: true, active: true, routing: emptyRouting, createdAt: '', updatedAt: '' } as SIPNumber

function renderEditor() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={qc}><RoutingEditor open onClose={vi.fn()} number={number} profiles={profiles} /></QueryClientProvider>)
}

beforeEach(() => { vi.clearAllMocks() })

describe('routing draft helpers', () => {
  it('round-trips a config and validates the menu', () => {
    const cfg: RoutingConfig = {
      businessHours: { timezone: 'Asia/Ulaanbaatar', weekdays: [1, 2, 3, 4, 5], startTime: '09:00', endTime: '18:00', pacePerMinute: 0 },
      afterHoursProfileId: 'p2', afterHoursMessage: '', menuPrompt: 'Дарна уу', menu: [{ key: '1', label: 'Борлуулалт', agentProfileId: 'p1' }], menuTimeoutSec: 10, menuRepeat: 2,
    }
    expect(routingToJSON(draftFromRouting(cfg))).toEqual(cfg)
    const bad = draftFromRouting({ ...cfg, menu: [{ key: '1', label: '', agentProfileId: '' }, { key: '1', label: 'x', agentProfileId: 'p1' }], menuTimeoutSec: 99 })
    const e = validateRouting(bad)
    expect(e.menu[0]).toEqual({ key: 'Товч давхардсан', label: 'Нэр оруулна уу', profile: 'Профайл сонгоно уу' })
    expect(e.timeout).toBeTruthy()
  })
})

describe('RoutingEditor', () => {
  it('produces the RoutingConfig JSON on save', async () => {
    mocked.put.mockResolvedValue({})
    renderEditor()

    // business hours: turn off 24/7 → default Mon-Fri 09:00-18:00
    fireEvent.click(screen.getByRole('switch', { name: '24/7' }))
    fireEvent.change(screen.getByLabelText('Эхлэх цаг'), { target: { value: '08:30' } })

    // after hours: message
    fireEvent.change(screen.getByLabelText(/^Ажлын бус цагийн мессеж/), { target: { value: 'Ажлын цаг дууссан' } })

    // menu
    fireEvent.click(screen.getByRole('button', { name: /Цэсийн мөр нэмэх/ }))
    fireEvent.change(screen.getByLabelText('Цэсийн нэр 1'), { target: { value: 'Борлуулалт' } })
    fireEvent.change(screen.getByLabelText('Цэсийн профайл 1'), { target: { value: 'p1' } })
    fireEvent.click(screen.getByRole('button', { name: /Цэсийн мөр нэмэх/ }))
    fireEvent.change(screen.getByLabelText('Товч 2'), { target: { value: '#' } })
    fireEvent.change(screen.getByLabelText('Цэсийн нэр 2'), { target: { value: 'Дэмжлэг' } })
    fireEvent.change(screen.getByLabelText('Цэсийн профайл 2'), { target: { value: 'p2' } })
    fireEvent.change(screen.getByLabelText(/^Цэсийн мэндчилгээ/), { target: { value: 'Борлуулалтад 0, дэмжлэгт # дарна уу' } })
    fireEvent.change(screen.getByLabelText(/^Товч хүлээх/), { target: { value: '12' } })
    fireEvent.change(screen.getByLabelText(/^Давтах тоо/), { target: { value: '2' } })

    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalled())
    expect(mocked.put).toHaveBeenCalledWith('/sip-numbers/n1/routing', {
      businessHours: { timezone: 'Asia/Ulaanbaatar', weekdays: [1, 2, 3, 4, 5], startTime: '08:30', endTime: '18:00', pacePerMinute: 0 },
      afterHoursProfileId: null, afterHoursMessage: 'Ажлын цаг дууссан',
      menuPrompt: 'Борлуулалтад 0, дэмжлэгт # дарна уу',
      menu: [{ key: '0', label: 'Борлуулалт', agentProfileId: 'p1' }, { key: '#', label: 'Дэмжлэг', agentProfileId: 'p2' }],
      menuTimeoutSec: 12, menuRepeat: 2,
    })
  })

  it('blocks save while a menu row is incomplete', async () => {
    renderEditor()
    fireEvent.click(screen.getByRole('button', { name: /Цэсийн мөр нэмэх/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    expect(await screen.findByText('Нэр оруулна уу')).toBeInTheDocument()
    expect(screen.getByText('Профайл сонгоно уу')).toBeInTheDocument()
    expect(mocked.put).not.toHaveBeenCalled()
  })

  it('previews the resolved route for a chosen time', async () => {
    mocked.post.mockResolvedValue({ route: { mode: 'after_hours', agentProfileId: 'p2', message: 'Дараа залгана уу' } })
    renderEditor()
    fireEvent.change(screen.getByLabelText(/^Дуудлага ирэх цаг/), { target: { value: '2026-10-03T22:15' } })
    fireEvent.click(screen.getByRole('button', { name: 'Шалгах' }))
    const result = await screen.findByTestId('route-result')
    expect(mocked.post).toHaveBeenCalledWith('/sip-numbers/n1/routing/resolve', undefined, { at: new Date('2026-10-03T22:15').toISOString() })
    expect(within(result).getByText('Ажлын бус цаг')).toBeInTheDocument()
    expect(within(result).getByText('Дэмжлэг')).toBeInTheDocument()
    expect(within(result).getByText('Дараа залгана уу')).toBeInTheDocument()
  })
})
