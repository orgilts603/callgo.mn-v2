import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { CallDrawer } from '../CallDrawer'
import { callKey, type CallDetail } from '../api'
import { connectFakeLive, createWaveSurferMock, makeCall, makeQueryClient, makeTurn, resetLive, wrapper } from '../testUtils'

const ws = vi.hoisted(() => ({ mock: null as null | ReturnType<typeof import('../testUtils').createWaveSurferMock> }))
vi.mock('wavesurfer.js', async () => {
  const { createWaveSurferMock: make } = await import('../testUtils')
  ws.mock = make()
  return { default: { create: ws.mock.create } }
})
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

const get = vi.mocked(api.get)
const post = vi.mocked(api.post)

function detail(over: Partial<CallDetail> = {}): CallDetail {
  return { call: makeCall(), turns: [makeTurn()], contact: { id: 'k1', orgId: 'o1', phone: '+97699112233', name: 'Бат-Эрдэнэ', tags: [], meta: {}, createdAt: '', updatedAt: '' }, ...over }
}

describe('CallDrawer', () => {
  beforeEach(() => { get.mockReset(); post.mockReset() })
  afterEach(resetLive)

  it('shows header, empty recording state and hangs up after confirm', async () => {
    get.mockImplementation(async (path: string) => {
      if (path === '/calls/c1') return detail()
      if (path === '/campaigns') return { items: [] }
      throw new Error(`unexpected ${path}`)
    })
    post.mockResolvedValue(undefined)
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })

    expect(await screen.findByText('Бат-Эрдэнэ')).toBeInTheDocument()
    expect(screen.getByText('+976 9911 2233', { exact: false })).toBeInTheDocument()
    expect(screen.getByTestId('audio-empty')).toHaveTextContent('Дуудлага дууссаны дараа бичлэг гарна.')

    fireEvent.click(screen.getByRole('button', { name: /Дуудлага таслах/ }))
    const dialog = screen.getByRole('dialog', { name: 'Дуудлага таслах уу?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Таслах' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/calls/c1/hangup'))
  })

  it('transfers to a number', async () => {
    get.mockResolvedValue(detail())
    post.mockResolvedValue(undefined)
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    fireEvent.click(await screen.findByRole('button', { name: /Шилжүүлэх/ }))
    fireEvent.change(screen.getByLabelText('Утасны дугаар'), { target: { value: '+976 8811 2233' } })
    fireEvent.click(within(screen.getByRole('dialog', { name: 'Дуудлага шилжүүлэх' })).getByRole('button', { name: 'Шилжүүлэх' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/calls/c1/transfer', { toNumber: '+97688112233' }))
  })

  it('renders the waveform player for recorded calls and seeks from the transcript', async () => {
    get.mockResolvedValue(detail({
      call: makeCall({ status: 'completed', durationSec: 95, recordingUrl: 'https://rec/x.ogg', summary: 'Захиалга өгсөн', sentiment: 'positive', intent: 'order', endReason: 'hangup_customer', llmModelUsed: 'google/gemini-2.5-flash' }),
    }))
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    expect(await screen.findByText('Захиалга өгсөн')).toBeInTheDocument()
    expect(screen.getByText('Харилцагч таслав')).toBeInTheDocument()
    expect(screen.getByText('Эерэг')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Дуудлага таслах/ })).toBeNull()
    expect(screen.getByRole('link', { name: /Бичлэг татах/ })).toHaveAttribute('href', 'https://rec/x.ogg')

    const m = ws.mock!
    expect(m.create).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://rec/x.ogg' }))
    act(() => m.instance.fire('ready', 42))
    fireEvent.click(screen.getByRole('button', { name: '0:01' }))
    expect(m.instance.setTime).toHaveBeenCalledWith(1.2)
    fireEvent.click(screen.getByRole('button', { name: '1.5x' }))
    expect(m.instance.setPlaybackRate).toHaveBeenCalledWith(1.5, true)
  })

  it('appends live final turns and shows partials + agent state', async () => {
    const live = connectFakeLive()
    get.mockResolvedValue(detail())
    const qc = makeQueryClient()
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(qc) })
    await screen.findByText('Бат-Эрдэнэ')

    act(() => {
      live.emit({ type: 'agent.state', callId: 'c1', payload: { state: 'thinking' } })
      live.emit({ type: 'transcript.partial', callId: 'c1', payload: { speaker: 'agent', text: 'Тэгэлгүй яах', startMs: 5000 } })
    })
    expect(screen.getByText('Бодож байна')).toBeInTheDocument()
    expect(screen.getByTestId('partial')).toHaveTextContent('Тэгэлгүй яах')

    const turn = makeTurn({ id: 't2', seq: 2, speaker: 'agent', text: 'Тэгэлгүй яах вэ.', startMs: 5000, endMs: 6000 })
    act(() => {
      live.emit({ type: 'transcript.final', callId: 'c1', payload: { turn } })
      live.emit({ type: 'transcript.final', callId: 'c1', payload: { turn } })
    })
    expect(screen.queryByTestId('partial')).toBeNull()
    expect(screen.getAllByTestId('turn')).toHaveLength(2)
    expect(qc.getQueryData<CallDetail>(callKey('c1'))?.turns.map((t) => t.id)).toEqual(['t1', 't2'])
  })
})
