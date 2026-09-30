import { describe, expect, it, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

const get = vi.fn()
vi.mock('@/lib/api', () => ({ api: { get: (...a: unknown[]) => get(...a) } }))
vi.mock('@/features/calls/CallDrawer', () => ({ CallDrawer: () => null }))

import { CallHistoryPage } from './CallHistoryPage'

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}><MemoryRouter><CallHistoryPage /></MemoryRouter></QueryClientProvider>,
  )
}

describe('CallHistoryPage', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockImplementation(async (path: string) => {
      if (path === '/campaigns') return { items: [{ id: 'camp1', name: 'Хямдрал' }] }
      return { items: [], total: 0 }
    })
  })

  it('passes filters as query params to api.get', async () => {
    renderPage()
    await waitFor(() => expect(get).toHaveBeenCalledWith('/calls', expect.objectContaining({ limit: 25, offset: 0 })))

    fireEvent.change(screen.getByLabelText('Хайх'), { target: { value: '9911' } })
    fireEvent.change(screen.getByLabelText('Чиглэл'), { target: { value: 'inbound' } })
    fireEvent.click(screen.getByRole('button', { name: 'Дууссан' }))
    fireEvent.click(screen.getByRole('button', { name: 'Амжилтгүй' }))
    await screen.findByRole('option', { name: 'Хямдрал' })
    fireEvent.change(screen.getByLabelText('Кампанит ажил'), { target: { value: 'camp1' } })

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/calls', expect.objectContaining({
        q: '9911', direction: 'inbound', status: 'completed,failed', campaignId: 'camp1', limit: 25, offset: 0,
      })),
    )
  })

  it('renders rows and total', async () => {
    get.mockImplementation(async (path: string) => {
      if (path === '/campaigns') return { items: [] }
      return {
        total: 1,
        items: [{ id: 'c1', direction: 'inbound', status: 'completed', fromNumber: '+97699112233', toNumber: '+97677001122', durationSec: 65, sentiment: 'positive', summary: 'Захиалга', startedAt: '2026-09-30T10:00:00Z', recordingUrl: 'http://x' }],
      }
    })
    renderPage()
    expect(await screen.findByText('+976 9911 2233')).toBeInTheDocument()
    expect(screen.getByText('1:05')).toBeInTheDocument()
    expect(screen.getByText('Нийт 1 дуудлага')).toBeInTheDocument()
  })
})
