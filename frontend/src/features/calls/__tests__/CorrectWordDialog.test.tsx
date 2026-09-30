import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { CorrectWordDialog } from '../CorrectWordDialog'
import { EditTurnDialog } from '../EditTurnDialog'
import { callKey, type CallDetail } from '../api'
import { makeCall, makeQueryClient, makeTurn, wrapper } from '../testUtils'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }))

const patch = vi.mocked(api.patch)

describe('CorrectWordDialog', () => {
  beforeEach(() => { patch.mockReset(); vi.mocked(toast.success).mockReset() })

  const turn = makeTurn({ id: 't1', text: 'Би колгоу, колгоу захиалъя.' })

  function setup(wordIndex: number) {
    const qc = makeQueryClient()
    qc.setQueryData<CallDetail>(callKey('c1'), { call: makeCall(), turns: [turn], contact: null })
    const onClose = vi.fn()
    render(<CorrectWordDialog open callId="c1" turn={turn} wordIndex={wordIndex} onClose={onClose} />, { wrapper: wrapper(qc) })
    return { qc, onClose }
  }

  it('sends text with the word replaced plus wrong/correct/scope/phonetic', async () => {
    const updated = { ...turn, text: 'Би колгоу, CallGo захиалъя.' }
    patch.mockResolvedValue({ turn: updated, correction: null })
    const { qc, onClose } = setup(2)

    fireEvent.change(screen.getByLabelText('Зөв бичлэг'), { target: { value: 'CallGo' } })
    fireEvent.change(screen.getByLabelText('Хамрах хүрээ'), { target: { value: 'both' } })
    fireEvent.change(screen.getByLabelText('Фонетик'), { target: { value: 'кол гоу' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))

    await waitFor(() => expect(patch).toHaveBeenCalledTimes(1))
    expect(patch).toHaveBeenCalledWith('/turns/t1', {
      text: 'Би колгоу, CallGo захиалъя.', wrong: 'колгоу', correct: 'CallGo', scope: 'both', phonetic: 'кол гоу',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Lexicon-д нэмэгдлээ'))
    expect(onClose).toHaveBeenCalled()
    expect(qc.getQueryData<CallDetail>(callKey('c1'))?.turns[0].text).toBe('Би колгоу, CallGo захиалъя.')
  })

  it('defaults scope to stt, omits empty phonetic and keeps punctuation', async () => {
    patch.mockResolvedValue({ turn, correction: null })
    setup(1)
    fireEvent.change(screen.getByLabelText('Зөв бичлэг'), { target: { value: 'CallGo' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(patch).toHaveBeenCalled())
    expect(patch).toHaveBeenCalledWith('/turns/t1', { text: 'Би CallGo, колгоу захиалъя.', wrong: 'колгоу', correct: 'CallGo', scope: 'stt' })
  })

  it('disables save while the correction equals the original word', () => {
    setup(1)
    expect(screen.getByRole('button', { name: 'Хадгалах' })).toBeDisabled()
  })
})

describe('EditTurnDialog', () => {
  it('PATCHes only {text}', async () => {
    const turn = makeTurn()
    patch.mockResolvedValue({ turn: { ...turn, text: 'Шинэ текст' }, correction: null })
    render(<EditTurnDialog open callId="c1" turn={turn} onClose={() => {}} />, { wrapper: wrapper(makeQueryClient()) })
    fireEvent.change(screen.getByLabelText('Текст'), { target: { value: '  Шинэ текст ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(patch).toHaveBeenCalledWith('/turns/t1', { text: 'Шинэ текст' }))
  })
})
