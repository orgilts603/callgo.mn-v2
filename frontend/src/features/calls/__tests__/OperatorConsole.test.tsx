import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { api, HttpError } from '@/lib/api'
import type { HandoffToken } from '@/lib/types'
import { OperatorConsole } from '../OperatorConsole'
import { FakeRoom, type LiveKitMock } from '../livekitTestUtils'
import { makeCall, makeQueryClient, makeTurn, wrapper } from '../testUtils'
import { CONNECT_FAILED_MESSAGE, MIC_DENIED_MESSAGE } from '../useLiveKitRoom'

vi.mock('livekit-client', async () => (await import('../livekitTestUtils')).createLiveKitMock())
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

const post = vi.mocked(api.post)
const token: HandoffToken = { token: 'jwt-token', url: 'wss://lk.example.mn', roomName: 'room-c1', identity: 'op-u1' }

async function lk() { return (await import('livekit-client')) as unknown as LiveKitMock }
const lastRoom = () => FakeRoom.instances[FakeRoom.instances.length - 1]

function renderConsole(onClose = vi.fn(), call = makeCall()) {
  const utils = render(<OperatorConsole call={call} turns={[makeTurn()]} onClose={onClose} />, { wrapper: wrapper(makeQueryClient()) })
  return { ...utils, onClose }
}

describe('OperatorConsole', () => {
  beforeEach(async () => {
    post.mockReset()
    FakeRoom.instances.length = 0
    FakeRoom.connectError = null
    const m = await lk()
    m.createLocalAudioTrack.mockClear()
    m.__mic.isMuted = false
    m.__mic.stop.mockClear(); m.__mic.mute.mockClear(); m.__mic.unmute.mockClear()
    post.mockImplementation(async (path: string) => (path.endsWith('/handoff') ? token : undefined))
  })

  it('requests a token, connects with url/token, publishes the mic and shows the transcript', async () => {
    const m = await lk()
    renderConsole()
    await waitFor(() => expect(screen.getByTestId('room-phase')).toHaveTextContent('Холбогдсон'))
    expect(post).toHaveBeenCalledWith('/calls/c1/handoff')
    expect(m.createLocalAudioTrack).toHaveBeenCalled()
    expect(lastRoom().connect).toHaveBeenCalledWith('wss://lk.example.mn', 'jwt-token')
    expect(lastRoom().localParticipant.publishTrack).toHaveBeenCalledWith(m.__mic)
    // live transcript stays visible beside the controls
    expect(screen.getByTestId('turn')).toHaveTextContent('колгоу')
  })

  it('lists participants and marks who is speaking', async () => {
    renderConsole()
    await waitFor(() => expect(screen.getByTestId('room-phase')).toHaveTextContent('Холбогдсон'))
    const room = lastRoom()
    const customer = { identity: 'sip_+97699112233', name: '+97699112233', kind: 3, attributes: {} }
    const agent = { identity: 'agent-1', name: 'AI', kind: 4, attributes: {} }
    act(() => {
      room.remoteParticipants.set(customer.identity, customer)
      room.remoteParticipants.set(agent.identity, agent)
      room.emit('participantConnected', customer)
      room.emit('participantConnected', agent)
    })
    const items = screen.getAllByTestId('participant')
    expect(items.map((li) => li.getAttribute('data-role'))).toEqual(['operator', 'customer', 'agent'])
    expect(items[0]).toHaveTextContent('(та)')

    act(() => room.emit('activeSpeakersChanged', [customer]))
    const rows = screen.getAllByTestId('participant')
    expect(rows.map((li) => li.getAttribute('data-speaking'))).toEqual(['false', 'true', 'false'])
    expect(within(rows[1]).getByText('ярьж байна')).toBeInTheDocument()
  })

  it('mutes and unmutes the microphone', async () => {
    const m = await lk()
    renderConsole()
    await waitFor(() => expect(screen.getByTestId('room-phase')).toHaveTextContent('Холбогдсон'))
    fireEvent.click(screen.getByRole('button', { name: 'Микрофон хаах' }))
    expect(m.__mic.mute).toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: 'Микрофон нээх' }))
    expect(m.__mic.unmute).toHaveBeenCalled()
  })

  it('attaches remote audio and applies the volume slider', async () => {
    renderConsole()
    await waitFor(() => expect(screen.getByTestId('room-phase')).toHaveTextContent('Холбогдсон'))
    const el = document.createElement('audio')
    const track = { kind: 'audio', attach: vi.fn(() => el), detach: vi.fn(() => [el]) }
    act(() => lastRoom().emit('trackSubscribed', track))
    expect(track.attach).toHaveBeenCalled()
    expect(screen.getByTestId('audio-sink')).toContainElement(el)
    fireEvent.change(screen.getByLabelText('Дууны түвшин'), { target: { value: '40' } })
    expect(el.volume).toBeCloseTo(0.4)
    act(() => lastRoom().emit('trackUnsubscribed', track))
    expect(screen.getByTestId('audio-sink')).not.toContainElement(el)
  })

  it('leaving disconnects, stops the mic and posts handoff/end', async () => {
    const m = await lk()
    const { onClose } = renderConsole()
    await waitFor(() => expect(screen.getByTestId('room-phase')).toHaveTextContent('Холбогдсон'))
    fireEvent.click(screen.getByRole('button', { name: /Гарах/ }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(lastRoom().disconnect).toHaveBeenCalled()
    expect(m.__mic.stop).toHaveBeenCalled()
    expect(post).toHaveBeenCalledWith('/calls/c1/handoff/end')
    expect(post.mock.calls.filter(([p]) => p === '/calls/c1/handoff/end')).toHaveLength(1)
  })

  it('hangs up the call through the confirm dialog', async () => {
    renderConsole()
    await waitFor(() => expect(screen.getByTestId('room-phase')).toHaveTextContent('Холбогдсон'))
    fireEvent.click(screen.getByRole('button', { name: /Дуудлага таслах/ }))
    fireEvent.click(within(screen.getByRole('dialog', { name: 'Дуудлага таслах уу?' })).getByRole('button', { name: 'Таслах' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/calls/c1/hangup'))
  })

  it('explains a denied microphone, never connects, and releases the handoff', async () => {
    const m = await lk()
    m.createLocalAudioTrack.mockRejectedValueOnce(Object.assign(new Error('denied'), { name: 'NotAllowedError' }))
    renderConsole()
    expect(await screen.findByRole('alert')).toHaveTextContent(MIC_DENIED_MESSAGE)
    expect(FakeRoom.instances).toHaveLength(0)
    await waitFor(() => expect(post).toHaveBeenCalledWith('/calls/c1/handoff/end'))
    expect(screen.getByTestId('room-phase')).toHaveTextContent('Алдаа')
  })

  it('shows a connection failure, releases the handoff and can retry', async () => {
    FakeRoom.connectError = new Error('signal failed')
    renderConsole()
    expect(await screen.findByRole('alert')).toHaveTextContent(CONNECT_FAILED_MESSAGE)
    await waitFor(() => expect(post).toHaveBeenCalledWith('/calls/c1/handoff/end'))
    expect((await lk()).__mic.stop).toHaveBeenCalled()

    FakeRoom.connectError = null
    post.mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'Дахин оролдох' }))
    await waitFor(() => expect(screen.getByTestId('room-phase')).toHaveTextContent('Холбогдсон'))
    expect(post).toHaveBeenCalledWith('/calls/c1/handoff')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('reports a handoff API failure (plan without the feature)', async () => {
    post.mockRejectedValueOnce(new HttpError(403, 'feature_unavailable', 'feature'))
    renderConsole()
    expect(await screen.findByRole('alert')).toHaveTextContent('таны багцад ороогүй')
    expect(FakeRoom.instances).toHaveLength(0)
    expect(post).not.toHaveBeenCalledWith('/calls/c1/handoff/end')
  })
})
