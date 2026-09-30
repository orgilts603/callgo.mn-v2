import { describe, expect, it, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const get = vi.fn()
const post = vi.fn()
vi.mock('@/lib/api', () => ({ api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a), put: vi.fn(), delete: vi.fn() } }))

import { LexiconPage, patchLexiconCache } from './LexiconPage'
import type { LexiconCorrection } from '@/lib/types'

const corr = (o: Partial<LexiconCorrection>): LexiconCorrection => ({
  id: 'l1', orgId: 'o', wrong: 'кол гоу', correct: 'CallGo', scope: 'both', hitCount: 3, createdAt: '2026-09-30T10:00:00Z', ...o,
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><LexiconPage /></QueryClientProvider>)
}

describe('LexiconPage', () => {
  beforeEach(() => {
    get.mockReset(); post.mockReset()
    get.mockResolvedValue({ items: [corr({})], total: 1 })
  })

  it('lists corrections', async () => {
    renderPage()
    expect(await screen.findByText('CallGo')).toBeInTheDocument()
    expect(screen.getByText('STT + TTS')).toBeInTheDocument()
  })

  it('apply panel highlights hits with <mark>', async () => {
    post.mockResolvedValue({ text: 'Сайн байна уу CallGo байна', hits: [{ id: 'l1', wrong: 'кол гоу', correct: 'CallGo' }] })
    renderPage()
    await screen.findByText('STT + TTS')
    fireEvent.change(screen.getByLabelText('Туршилтын текст'), { target: { value: 'Сайн байна уу кол гоу байна' } })
    fireEvent.click(screen.getByRole('button', { name: /Туршиж үзэх/ }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/lexicon/apply', { text: 'Сайн байна уу кол гоу байна' }))
    const result = await screen.findByTestId('apply-result')
    const marks = result.querySelectorAll('mark')
    expect(marks).toHaveLength(1)
    expect(marks[0].textContent).toBe('CallGo')
  })

  it('patchLexiconCache handles created/updated/deleted', () => {
    const base = { items: [corr({})], total: 1 }
    const created = patchLexiconCache(base, { action: 'created', correction: corr({ id: 'l2', wrong: 'a' }) })
    expect(created?.total).toBe(2)
    expect(created?.items[0].id).toBe('l2')
    const updated = patchLexiconCache(base, { action: 'updated', correction: corr({ correct: 'X' }) })
    expect(updated?.items[0].correct).toBe('X')
    const deleted = patchLexiconCache(base, { action: 'deleted', correction: corr({}) })
    expect(deleted?.items).toHaveLength(0)
    expect(deleted?.total).toBe(0)
  })
})
