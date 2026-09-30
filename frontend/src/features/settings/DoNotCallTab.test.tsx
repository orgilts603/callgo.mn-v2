import type { ReactNode } from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DoNotCallEntry } from '@/lib/types'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

import { api } from '@/lib/api'
import DoNotCallTab from './DoNotCallTab'
import SettingsPage from './SettingsPage'

const mocked = vi.mocked(api)
const entry = (phone: string, reason = ''): DoNotCallEntry => ({ id: `id-${phone}`, orgId: 'o', phone, reason, createdAt: '2026-01-02T03:04:05Z' })

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  mocked.get.mockResolvedValue({ items: [entry('+97699112233', 'Татгалзсан'), entry('+97688001122')], total: 2 })
})

describe('DoNotCallTab', () => {
  it('lists entries, explains the purpose and queries with search + pagination params', async () => {
    wrap(<DoNotCallTab />)
    expect(await screen.findByText('+976 9911 2233')).toBeInTheDocument()
    expect(screen.getByText('Татгалзсан')).toBeInTheDocument()
    expect(screen.getByText(/Хууль эрх зүй/)).toBeInTheDocument()
    expect(screen.getByText(/Брэнд/)).toBeInTheDocument()
    expect(mocked.get).toHaveBeenCalledWith('/dnc', { q: '', limit: 25, offset: 0 })

    fireEvent.change(screen.getByLabelText('Хайх'), { target: { value: '9911' } })
    await waitFor(() => expect(mocked.get).toHaveBeenCalledWith('/dnc', { q: '9911', limit: 25, offset: 0 }))
  })

  it('paginates', async () => {
    mocked.get.mockResolvedValue({ items: [entry('+97699112233')], total: 60 })
    wrap(<DoNotCallTab />)
    await screen.findByText('+976 9911 2233')
    fireEvent.click(screen.getByRole('button', { name: 'Дараах' }))
    await waitFor(() => expect(mocked.get).toHaveBeenCalledWith('/dnc', { q: '', limit: 25, offset: 25 }))
  })

  it('adds a number (8 digits get +976) with a reason', async () => {
    mocked.post.mockResolvedValue({ entry: entry('+97670001234') })
    wrap(<DoNotCallTab />)
    fireEvent.click(await screen.findByRole('button', { name: /Дугаар нэмэх/ }))
    const dialog = screen.getByRole('dialog', { name: 'Хориглосон дугаар нэмэх' })
    fireEvent.change(within(dialog).getByLabelText(/^Дугаар/), { target: { value: '7000 1234' } })
    fireEvent.change(within(dialog).getByLabelText(/^Шалтгаан/), { target: { value: 'Хүсэлтээр' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Нэмэх' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/dnc', { phone: '+97670001234', reason: 'Хүсэлтээр' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Хориглосон дугаар нэмэх' })).toBeNull())
  })

  it('rejects an invalid number without calling the API', async () => {
    wrap(<DoNotCallTab />)
    fireEvent.click(await screen.findByRole('button', { name: /Дугаар нэмэх/ }))
    const dialog = screen.getByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText(/^Дугаар/), { target: { value: '12' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Нэмэх' }))
    expect(await within(dialog).findByText(/E\.164 форматтай/)).toBeInTheDocument()
    expect(mocked.post).not.toHaveBeenCalled()
  })

  it('deletes with confirm using the URL-encoded phone', async () => {
    mocked.delete.mockResolvedValue(undefined)
    wrap(<DoNotCallTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Устгах +97699112233' }))
    expect(mocked.delete).not.toHaveBeenCalled()
    const dialog = screen.getByRole('dialog', { name: 'Жагсаалтаас хасах уу?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Хасах' }))
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith('/dnc/%2B97699112233'))
  })

  it('imports a file as multipart and shows the counts', async () => {
    mocked.post.mockResolvedValue({ imported: 12, skipped: 3, errors: [{ row: 5, message: 'bad phone' }] })
    wrap(<DoNotCallTab />)
    fireEvent.click(await screen.findByRole('button', { name: /Импортлох/ }))
    const file = new File(['phone\n99112233\n'], 'dnc.xlsx')
    fireEvent.change(screen.getByLabelText('Импортлох файл'), { target: { files: [file] } })
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Импортлох' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledTimes(1))
    const [path, body] = mocked.post.mock.calls[0]
    expect(path).toBe('/dnc/import')
    expect((body as FormData).get('file')).toBe(file)
    const res = await screen.findByTestId('dnc-import-result')
    expect(res).toHaveTextContent('12')
    expect(res).toHaveTextContent('Импортолсон')
    expect(res).toHaveTextContent('Алгассан')
    expect(res).toHaveTextContent('bad phone')
  })

  it('is registered as the "Хориглосон дугаар" tab at /settings/dnc', async () => {
    wrap(
      <MemoryRouter initialEntries={['/settings/dnc']}>
        <Routes><Route path="/settings/:tab" element={<SettingsPage />} /></Routes>
      </MemoryRouter>,
    )
    expect(screen.getByRole('link', { name: /Хориглосон дугаар/ })).toHaveAttribute('href', '/settings/dnc')
    expect(await screen.findByText('Хориглосон дугаарууд')).toBeInTheDocument()
  })
})
