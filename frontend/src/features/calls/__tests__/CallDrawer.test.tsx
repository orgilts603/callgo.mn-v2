import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api } from '@/lib/api'
import { CallDrawer } from '../CallDrawer'
import { callKey, type CallDetail } from '../api'
import { useAuth } from '@/app/auth'
import { testUser } from '@/app/testing'
import { connectFakeLive, makeCall, makeQueryClient, makeTurn, resetLive, wrapper } from '../testUtils'

const ws = vi.hoisted(() => ({ mock: null as null | ReturnType<typeof import('../testUtils').createWaveSurferMock> }))
vi.mock('wavesurfer.js', async () => {
  const { createWaveSurferMock: make } = await import('../testUtils')
  ws.mock = make()
  return { default: { create: ws.mock.create } }
})
vi.mock('livekit-client', async () => (await import('../livekitTestUtils')).createLiveKitMock())
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

  it('adds the customer number to the do-not-call list after confirm', async () => {
    get.mockResolvedValue(detail())
    post.mockResolvedValue(undefined)
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    fireEvent.click(await screen.findByRole('button', { name: /Хориглох жагсаалтад нэмэх/ }))
    expect(post).not.toHaveBeenCalled()
    const dialog = screen.getByRole('dialog', { name: 'Хориглох жагсаалтад нэмэх үү?' })
    expect(dialog).toHaveTextContent('+976 9911 2233') // inbound: the caller is the customer
    fireEvent.change(within(dialog).getByLabelText('Шалтгаан'), { target: { value: 'Хүсэлтээр' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Нэмэх' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/calls/c1/dnc', { reason: 'Хүсэлтээр' }))
    const { toast } = await import('sonner')
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(expect.stringContaining('хориглох жагсаалтад')))
  })

  it('uses the callee as the customer number on outbound calls and hides the action without one', async () => {
    get.mockResolvedValue(detail({ call: makeCall({ direction: 'outbound', fromNumber: '+97677001100', toNumber: '+97688112233', status: 'completed' }) }))
    post.mockResolvedValue(undefined)
    const { unmount } = render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    fireEvent.click(await screen.findByRole('button', { name: /Хориглох жагсаалтад нэмэх/ }))
    expect(screen.getByRole('dialog', { name: 'Хориглох жагсаалтад нэмэх үү?' })).toHaveTextContent('+976 8811 2233')
    unmount()

    get.mockResolvedValue(detail({ call: makeCall({ direction: 'inbound', fromNumber: '', status: 'completed' }) }))
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    await screen.findByText('Бат-Эрдэнэ')
    expect(screen.queryByRole('button', { name: /Хориглох жагсаалтад нэмэх/ })).toBeNull()
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
    get.mockImplementation(async (path: string) => {
      if (path === '/calls/c1/recording/url') return { url: 'https://rec/x.ogg', expiresAt: new Date(Date.now() + 600_000).toISOString() }
      if (path === '/calls/c1') {
        return detail({
          call: makeCall({
            status: 'completed', durationSec: 95, recordingUrl: '/api/calls/c1/recording', recording: { status: 'ready', sizeBytes: 5000, durationSec: 95 },
            summary: 'Захиалга өгсөн', sentiment: 'positive', intent: 'order', endReason: 'hangup_customer', llmModelUsed: 'google/gemini-2.5-flash',
            usage: { llmTokensIn: 1200, llmTokensOut: 300, sttSeconds: 45, ttsChars: 800, costMnt: 1234, llmModel: 'google/gemini-2.5-flash' },
          }),
        })
      }
      throw new Error(`unexpected ${path}`)
    })
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    expect(await screen.findByText('Захиалга өгсөн')).toBeInTheDocument()
    expect(screen.getByText('Харилцагч таслав')).toBeInTheDocument()
    expect(screen.getByText('Эерэг')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Дуудлага таслах/ })).toBeNull()
    expect(screen.getByTestId('usage-line')).toHaveTextContent('Зардал: 1 234 ₮')
    expect(screen.getByTestId('usage-line')).toHaveTextContent('LLM 1 200/300 токен')
    expect(await screen.findByRole('link', { name: /Бичлэг татах/ })).toHaveAttribute('href', 'https://rec/x.ogg')

    const m = ws.mock!
    await waitFor(() => expect(m.create).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://rec/x.ogg' })))
    act(() => m.instance.fire('ready', 42))
    fireEvent.click(screen.getByRole('button', { name: '0:01' }))
    expect(m.instance.setTime).toHaveBeenCalledWith(1.2)
    fireEvent.click(screen.getByRole('button', { name: '1.5x' }))
    expect(m.instance.setPlaybackRate).toHaveBeenCalledWith(1.5, true)
  })

  it('shows a pulsing recording state for an in-progress recording', async () => {
    get.mockResolvedValue(detail({ call: makeCall({ recording: { status: 'recording', sizeBytes: 0, durationSec: 0 } }) }))
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    expect(await screen.findByTestId('audio-recording')).toBeInTheDocument()
  })

  it('shows the handoff badge with the operator and offers the take-over console for active calls', async () => {
    useAuth.setState({ user: testUser })
    get.mockResolvedValue(detail({ call: makeCall({ handoff: 'active', operatorId: testUser.id }) }))
    post.mockImplementation(async (path: string) => (path.endsWith('/handoff') ? { token: 't', url: 'wss://lk', roomName: 'r', identity: 'op-u1' } : undefined))
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    expect(await screen.findByTestId('handoff-badge')).toHaveTextContent('Оператор ярьж байна · Та')

    fireEvent.click(screen.getByRole('button', { name: /Дуудлагад орох/ }))
    const consoleDialog = await screen.findByRole('dialog', { name: 'Оператор дуудлагад орсон' })
    await waitFor(() => expect(post).toHaveBeenCalledWith('/calls/c1/handoff'))
    fireEvent.click(within(consoleDialog).getByRole('button', { name: /Гарах/ }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/calls/c1/handoff/end'))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Оператор дуудлагад орсон' })).toBeNull())
    useAuth.setState({ user: null })
  })

  it('disables take-over when the plan lacks the handoff feature and hides it for finished calls', async () => {
    get.mockImplementation(async (path: string) => {
      if (path === '/billing/subscription') return { limits: { features: ['recordings'] } }
      return detail()
    })
    const { unmount } = render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    const btn = await screen.findByRole('button', { name: /Дуудлагад орох/ })
    await waitFor(() => expect(btn).toBeDisabled())
    unmount()

    get.mockResolvedValue(detail({ call: makeCall({ status: 'completed' }) }))
    render(<CallDrawer callId="c1" onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    await screen.findByText('Бат-Эрдэнэ')
    expect(screen.queryByRole('button', { name: /Дуудлагад орох/ })).toBeNull()
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
