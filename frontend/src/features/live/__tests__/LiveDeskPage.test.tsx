import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type { CallStats } from '@/lib/types'
import { LiveDeskPage } from '../LiveDeskPage'
import { connectFakeLive, makeCall, makeQueryClient, makeTurn, resetLive, wrapper } from '@/features/calls/testUtils'

vi.mock('wavesurfer.js', async () => {
  const { createWaveSurferMock } = await import('@/features/calls/testUtils')
  return { default: { create: createWaveSurferMock().create } }
})
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

const stats: CallStats = {
  totalCalls: 120, activeCalls: 1, completedToday: 37, avgDurationSec: 154, positiveRatio: 0.6, negativeRatio: 0.1, inboundToday: 20, outboundToday: 17,
}
const get = vi.mocked(api.get)

function mockApi(active = [] as ReturnType<typeof makeCall>[]) {
  get.mockImplementation(async (path: string) => {
    switch (path) {
      case '/stats': return stats
      case '/calls/active': return { items: active }
      case '/campaigns': return { items: [{ id: 'camp1', name: 'Хаврын урамшуулал' }] }
      case '/contacts/k1': return { contact: { id: 'k1', name: 'Болд' } }
      default:
        if (path.startsWith('/calls/')) return { call: makeCall({ id: path.split('/')[2] }), turns: [], contact: null }
        throw new Error(`unexpected GET ${path}`)
    }
  })
}

function rows() { return screen.queryAllByTestId('live-row') }

describe('LiveDeskPage', () => {
  beforeEach(() => { get.mockReset() })
  afterEach(resetLive)

  it('shows the empty state and stats', async () => {
    mockApi()
    render(<LiveDeskPage />, { wrapper: wrapper(makeQueryClient(), ['/live']) })
    expect(await screen.findByText('Одоогоор идэвхтэй дуудлага алга')).toBeInTheDocument()
    expect(screen.getByText(/CALLGO_SIMULATOR/)).toBeInTheDocument()
    expect(await screen.findByText('37')).toBeInTheDocument()
    expect(screen.getByText('2:34')).toBeInTheDocument()
    expect(screen.getByTestId('ws-status')).toHaveTextContent('Холболт салсан')
  })

  it('renders calls from the store and updates on live events', async () => {
    mockApi()
    const live = connectFakeLive()
    render(<LiveDeskPage />, { wrapper: wrapper(makeQueryClient(), ['/live']) })
    await screen.findByText('Одоогоор идэвхтэй дуудлага алга')
    expect(screen.getByTestId('ws-status')).toHaveTextContent('Шууд холбогдсон')

    // Drive the zustand store directly.
    act(() => {
      useLive.setState({
        activeCalls: {
          c1: makeCall({ id: 'c1', contactId: 'k1', campaignId: 'camp1', status: 'ringing' }),
          c2: makeCall({ id: 'c2', fromNumber: '+97688000000', direction: 'outbound' }),
        },
      })
    })
    expect(rows()).toHaveLength(2)
    const row1 = () => rows().find((r) => r.dataset.callId === 'c1')!
    expect(within(row1()).getByText('Дуугарч байна')).toBeInTheDocument()
    expect(await within(row1()).findByText('Болд')).toBeInTheDocument()
    expect(await within(row1()).findByText('Хаврын урамшуулал')).toBeInTheDocument()

    // Events through the socket: answer, agent state, transcript.
    act(() => {
      live.emit({ type: 'call.answered', callId: 'c1', payload: { call: makeCall({ id: 'c1', contactId: 'k1', campaignId: 'camp1', status: 'active' }) } })
      live.emit({ type: 'agent.state', callId: 'c1', payload: { state: 'thinking' } })
      live.emit({ type: 'transcript.partial', callId: 'c1', payload: { speaker: 'customer', text: 'үнэ хэд вэ', startMs: 0 } })
    })
    expect(within(row1()).getByText('Ярьж байна')).toBeInTheDocument()
    expect(within(row1()).getByText('Бодож байна')).toBeInTheDocument()
    expect(within(row1()).getByText('үнэ хэд вэ')).toBeInTheDocument()

    act(() => { live.emit({ type: 'transcript.final', callId: 'c1', payload: { turn: makeTurn({ callId: 'c1', text: 'Үнэ нь хэд вэ?' }) } }) })
    expect(within(row1()).getByText('Үнэ нь хэд вэ?')).toBeInTheDocument()

    const feed = screen.getByTestId('event-feed')
    expect(within(feed).getByText('Хариуллаа')).toBeInTheDocument()
    expect(within(feed).queryByText('үнэ хэд вэ')).toBeNull() // partials are not in the feed

    act(() => {
      live.emit({ type: 'call.ended', callId: 'c2', payload: { call: makeCall({ id: 'c2', status: 'completed' }), endReason: 'hangup_customer' } })
    })
    expect(rows()).toHaveLength(1)
    expect(screen.getByText('1', { selector: 'span.rounded-full' })).toBeInTheDocument()
  })

  it('merges the REST snapshot, sorts and opens the drawer on row click', async () => {
    mockApi([
      makeCall({ id: 'a', fromNumber: '+97699000002' }),
      makeCall({ id: 'b', fromNumber: '+97699000001' }),
      makeCall({ id: 'gone', status: 'completed' }),
    ])
    render(<LiveDeskPage />, { wrapper: wrapper(makeQueryClient(), ['/live']) })
    await waitFor(() => expect(rows()).toHaveLength(2))

    fireEvent.click(screen.getByRole('button', { name: /Хаанаас/ }))
    expect(rows().map((r) => r.dataset.callId)).toEqual(['b', 'a'])
    fireEvent.click(screen.getByRole('button', { name: /Хаанаас/ }))
    expect(rows().map((r) => r.dataset.callId)).toEqual(['a', 'b'])

    fireEvent.click(rows()[0])
    await waitFor(() => expect(get).toHaveBeenCalledWith('/calls/a'))
  })

  it('collapses the event feed', async () => {
    mockApi()
    render(<LiveDeskPage />, { wrapper: wrapper(makeQueryClient(), ['/live']) })
    fireEvent.click(screen.getByRole('button', { name: 'Үйл явдлууд хураах' }))
    expect(screen.queryByTestId('event-feed')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Үйл явдлууд нээх' }))
    expect(screen.getByTestId('event-feed')).toBeInTheDocument()
  })
})
