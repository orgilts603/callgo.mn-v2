import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { AgentProfile, Campaign, SIPNumber } from '@/lib/types'
import { CampaignsPage } from './CampaignsPage'

const base = { orgId: 'o', script: '', maxAttempts: 2, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }
const campaigns = [
  { ...base, id: 'c1', name: 'Хураамж', status: 'running', sipNumberId: 'sip-1', agentProfileId: 'ap-1', concurrency: 5, total: 20, completed: 8, failed: 2 },
  { ...base, id: 'c2', name: 'Санал асуулга', status: 'draft', sipNumberId: null, agentProfileId: null, concurrency: 2, total: 0, completed: 0, failed: 0 },
] as unknown as Campaign[]

function mockApi() {
  vi.spyOn(api, 'get').mockImplementation(async (path: string) => {
    if (path === '/campaigns') return { items: campaigns } as never
    if (path === '/sip-numbers') return { items: [{ id: 'sip-1', label: 'Үндсэн шугам', number: '+97677001122' }] as SIPNumber[] } as never
    if (path === '/agent-profiles') return { items: [{ id: 'ap-1', name: 'Борлуулагч' }] as AgentProfile[] } as never
    return { items: [] } as never
  })
}
function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(<QueryClientProvider client={qc}><MemoryRouter><CampaignsPage /></MemoryRouter></QueryClientProvider>)
}

describe('CampaignsPage', () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks() })

  it('renders rows with joined labels and progress', async () => {
    mockApi()
    renderPage()
    const row = (await screen.findByText('Хураамж')).closest('tr')!
    expect(within(row).getByText('Үндсэн шугам')).toBeInTheDocument()
    expect(within(row).getByText('Борлуулагч')).toBeInTheDocument()
    expect(within(row).getByText('50%')).toBeInTheDocument()
    expect(within(row).getByText('10/20')).toBeInTheDocument()
    expect(within(row).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '50')
    expect(within(row).getByText('Ажиллаж байна')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: /Түр зогсоох/ })).toBeInTheDocument()
    expect(within(row).queryByRole('button', { name: 'Устгах' })).toBeNull()
    const draft = screen.getByText('Санал асуулга').closest('tr')!
    expect(within(draft).getByRole('button', { name: /Эхлүүлэх/ })).toBeInTheDocument()
    expect(within(draft).getByRole('button', { name: 'Устгах' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Шинэ кампанит ажил/ })).toBeInTheDocument()
  })
})
