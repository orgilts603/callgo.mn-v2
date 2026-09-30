import { fireEvent, render, screen, within } from '@testing-library/react'
import { Transcript } from '../Transcript'
import { makeQueryClient, makeTurn, wrapper } from '../testUtils'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

describe('Transcript', () => {
  const turns = [
    makeTurn({ id: 't2', seq: 2, speaker: 'agent', text: 'Тийм ээ, тусалъя.', startMs: 4000, endMs: 5200 }),
    makeTurn({ id: 't1', seq: 1, speaker: 'customer', text: 'Би колгоу захиалмаар байна.', confidence: 0.4 }),
  ]

  it('renders turns (as given order), speakers and low-confidence tint', () => {
    render(<Transcript callId="c1" turns={[...turns].sort((a, b) => a.seq - b.seq)} />, { wrapper: wrapper(makeQueryClient()) })
    const rendered = screen.getAllByTestId('turn')
    expect(rendered).toHaveLength(2)
    expect(rendered[0]).toHaveAttribute('data-speaker', 'customer')
    expect(rendered[1]).toHaveAttribute('data-speaker', 'agent')
    expect(within(rendered[0]).getByText('40%')).toBeInTheDocument()
    expect(within(rendered[1]).getByText('Тийм ээ, тусалъя.')).toBeInTheDocument()
    // Agent words are not individually clickable.
    expect(within(rendered[1]).queryByRole('button', { name: 'тусалъя.' })).toBeNull()
  })

  it('opens the correction dialog with the clicked word', () => {
    render(<Transcript callId="c1" turns={turns} />, { wrapper: wrapper(makeQueryClient()) })
    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'колгоу' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('Үг засах')).toBeInTheDocument()
    expect(within(dialog).getByTestId('wrong-word')).toHaveTextContent('колгоу')
    expect(within(dialog).getByLabelText('Зөв бичлэг')).toHaveValue('колгоу')
  })

  it('strips punctuation from the clicked word', () => {
    render(<Transcript callId="c1" turns={turns} />, { wrapper: wrapper(makeQueryClient()) })
    fireEvent.click(screen.getByRole('button', { name: 'байна.' }))
    expect(screen.getByTestId('wrong-word')).toHaveTextContent(/^байна$/)
  })

  it('opens the full-turn editor from the pencil icon', () => {
    render(<Transcript callId="c1" turns={turns} />, { wrapper: wrapper(makeQueryClient()) })
    fireEvent.click(screen.getAllByRole('button', { name: 'Мөр засах' })[0])
    expect(screen.getByLabelText('Текст')).toHaveValue('Тийм ээ, тусалъя.')
  })

  it('shows partials as ghost bubbles and seeks on timestamp click', () => {
    const onSeek = vi.fn()
    render(<Transcript callId="c1" turns={turns} onSeek={onSeek} partials={[{ speaker: 'customer', text: 'одоо ярьж', startMs: 6000 }]} />,
      { wrapper: wrapper(makeQueryClient()) })
    expect(screen.getByTestId('partial')).toHaveTextContent('одоо ярьж')
    fireEvent.click(screen.getByRole('button', { name: '0:04' }))
    expect(onSeek).toHaveBeenCalledWith(4000)
  })

  it('shows the live empty state', () => {
    render(<Transcript callId="c1" turns={[]} live />, { wrapper: wrapper(makeQueryClient()) })
    expect(screen.getByText('Яриа эхлэхийг хүлээж байна')).toBeInTheDocument()
  })
})
