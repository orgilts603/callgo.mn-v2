import { configure, act, render, screen, waitFor } from '@testing-library/react'
import { api, HttpError } from '@/lib/api'
import type { RecordingInfo } from '@/lib/types'
import { AudioPlayer } from '../AudioPlayer'
import { makeQueryClient, wrapper } from '../testUtils'

// jsdom + recharts/wavesurfer are slow when the CI box is busy.
vi.setConfig({ testTimeout: 30_000 })
configure({ asyncUtilTimeout: 10_000 })

const ws = vi.hoisted(() => ({ mock: null as null | ReturnType<typeof import('../testUtils').createWaveSurferMock> }))
vi.mock('wavesurfer.js', async () => {
  const { createWaveSurferMock: make } = await import('../testUtils')
  ws.mock = make()
  return { default: { create: ws.mock.create } }
})
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

const get = vi.mocked(api.get)
const rec = (status: RecordingInfo['status']): RecordingInfo => ({ status, sizeBytes: 1000, durationSec: 30 })
const signed = (n: number) => ({ url: `https://rec.example/c1-${n}.ogg`, expiresAt: new Date(Date.now() + 600_000).toISOString() })

const renderPlayer = (props: Partial<React.ComponentProps<typeof AudioPlayer>> = {}) =>
  render(<AudioPlayer callId="c1" recording={rec('ready')} {...props} />, { wrapper: wrapper(makeQueryClient()) })

describe('AudioPlayer', () => {
  beforeEach(() => { get.mockReset(); ws.mock!.create.mockClear(); ws.mock!.instance.destroy.mockClear(); ws.mock!.instance.setTime.mockClear() })

  it('requests a signed url for a ready recording and loads it into the waveform', async () => {
    get.mockResolvedValue(signed(1))
    renderPlayer()
    await waitFor(() => expect(ws.mock!.create).toHaveBeenCalledWith(expect.objectContaining({ url: 'https://rec.example/c1-1.ogg' })))
    expect(get).toHaveBeenCalledWith('/calls/c1/recording/url')
    expect(get).toHaveBeenCalledTimes(1)
    act(() => ws.mock!.instance.fire('ready', 30))
    expect(screen.getByRole('button', { name: 'Тоглуулах' })).toBeEnabled()
  })

  it('shows a pulsing state while the recording is in progress and asks for no url', () => {
    renderPlayer({ recording: rec('recording'), live: true })
    expect(screen.getByTestId('audio-recording')).toHaveTextContent('Бичлэг хийгдэж байна')
    expect(get).not.toHaveBeenCalled()
  })

  it('shows the failed state', () => {
    renderPlayer({ recording: rec('failed') })
    expect(screen.getByTestId('audio-failed')).toHaveTextContent('Бичлэг хийж чадсангүй')
    expect(get).not.toHaveBeenCalled()
  })

  it('shows the empty state when there is no recording', () => {
    const { unmount } = renderPlayer({ recording: null, recordingUrl: undefined, live: true })
    expect(screen.getByTestId('audio-empty')).toHaveTextContent('Дуудлага дууссаны дараа бичлэг гарна.')
    unmount()
    renderPlayer({ recording: null, recordingUrl: undefined })
    expect(screen.getByTestId('audio-empty')).toHaveTextContent('Энэ дуудлагад бичлэг байхгүй.')
    expect(get).not.toHaveBeenCalled()
  })

  it('treats a legacy recordingUrl without recording info as playable', async () => {
    get.mockResolvedValue(signed(1))
    renderPlayer({ recording: undefined, recordingUrl: '/api/calls/c1/recording' })
    await waitFor(() => expect(ws.mock!.create).toHaveBeenCalled())
  })

  it('refreshes the signed url once when playback fails (expired link) and resumes', async () => {
    get.mockResolvedValueOnce(signed(1)).mockResolvedValueOnce(signed(2))
    renderPlayer()
    await waitFor(() => expect(ws.mock!.create).toHaveBeenCalledTimes(1))
    act(() => ws.mock!.instance.fire('ready', 30))
    act(() => ws.mock!.instance.fire('timeupdate', 12.4))
    act(() => ws.mock!.instance.fire('error', new Error('403 Forbidden')))
    await waitFor(() => expect(ws.mock!.create).toHaveBeenLastCalledWith(expect.objectContaining({ url: 'https://rec.example/c1-2.ogg' })))
    expect(get).toHaveBeenCalledTimes(2)
    act(() => ws.mock!.instance.fire('ready', 30))
    expect(ws.mock!.instance.setTime).toHaveBeenCalledWith(12.4)
  })

  it('explains a plan without recordings and a missing file', async () => {
    get.mockRejectedValueOnce(new HttpError(403, 'feature_unavailable', 'no'))
    const { unmount } = renderPlayer()
    expect(await screen.findByTestId('audio-error')).toHaveTextContent('таны багцад ороогүй')
    unmount()
    get.mockRejectedValueOnce(new HttpError(404, 'not_found', 'no'))
    renderPlayer()
    expect(await screen.findByTestId('audio-empty')).toHaveTextContent('Энэ дуудлагад бичлэг байхгүй.')
  })
})
