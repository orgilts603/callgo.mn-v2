import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { DEFAULT_OUTCOMES, type Campaign, type CampaignStats, type CampaignTarget } from '@/lib/types'
import { CampaignDetailPage } from './CampaignDetailPage'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const now = new Date().toISOString()
const baseCampaign: Campaign = {
  id: 'c1', orgId: 'o', name: 'Хураамж', script: 'Сайн уу', sipNumberId: 's', agentProfileId: 'a', status: 'draft', concurrency: 3, maxAttempts: 2,
  schedule: { timezone: 'Asia/Ulaanbaatar', weekdays: [1, 2, 3, 4, 5], startTime: '09:00', endTime: '18:00', pacePerMinute: 10 },
  outcomes: DEFAULT_OUTCOMES, dryRunLimit: 0, dryRunDialed: 0, total: 10, completed: 4, failed: 1, skipped: 2, createdAt: now, updatedAt: now,
}
const targets: CampaignTarget[] = [
  { id: 't1', campaignId: 'c1', phone: '+97699112233', name: 'Бат', status: 'done', attempts: 1, callId: 'call1', outcome: 'agreed', outcomeNote: 'Төлбөрөө маргааш төлнө', updatedAt: now },
  { id: 't2', campaignId: 'c1', phone: '+97688112233', name: 'Сараа', status: 'pending', attempts: 1, outcome: 'callback', outcomeNote: '', updatedAt: now },
  { id: 't3', campaignId: 'c1', phone: '+97677112233', name: '', status: 'skipped', attempts: 0, lastError: 'do-not-call', updatedAt: now },
]
const stats: CampaignStats = {
  byStatus: { pending: 3, calling: 1, done: 4, failed: 1, skipped: 2 },
  byOutcome: [{ code: 'agreed', label: 'Зөвшөөрсөн', count: 3 }, { code: 'declined', label: 'Татгалзсан', count: 1 }, { code: 'callback', label: 'Дахин залгах', count: 2 }],
}

function setup(campaign: Partial<Campaign> = {}) {
  const c = { ...baseCampaign, ...campaign }
  vi.spyOn(api, 'get').mockImplementation(async (path: string) => {
    if (path === '/campaigns/c1') return { campaign: c, targets: { items: targets, total: 3 } } as never
    if (path === '/campaigns/c1/stats') return stats as never
    return { items: [] } as never
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/campaigns/c1']}>
        <Routes><Route path="/campaigns/:id" element={<CampaignDetailPage />} /></Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return c
}

describe('CampaignDetailPage v2', () => {
  beforeEach(() => { localStorage.clear() })
  afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

  it('uses /stats for the stat cards (incl. Алгассан) and renders the outcome bars', async () => {
    setup({ status: 'running' })
    await screen.findByText('Бат')
    const card = (label: string) => screen.getAllByText(label, { selector: 'span' })[0].closest('div.px-4') as HTMLElement
    await waitFor(() => expect(within(card('Хүлээгдэж буй')).getByText('3')).toBeInTheDocument())
    expect(within(card('Алгассан')).getByText('2')).toBeInTheDocument()
    expect(within(card('Явж байгаа')).getByText('1')).toBeInTheDocument()
    expect(vi.mocked(api.get)).toHaveBeenCalledWith('/campaigns/c1/stats')

    const bars = await screen.findAllByTestId('outcome-bar')
    expect(bars).toHaveLength(5)
    expect(within(bars[0]).getByText('Зөвшөөрсөн')).toBeInTheDocument()
    expect(within(bars[0]).getByText('3')).toBeInTheDocument()
    expect(within(bars[2]).getByText('Дахин залгах')).toBeInTheDocument()
    expect(within(bars[2]).getByText('2')).toBeInTheDocument()
    expect(within(bars[4]).getByText('0')).toBeInTheDocument()
  })

  it('shows Үр дүн and Тайлбар columns, skipped status and filter option', async () => {
    setup()
    const row = (await screen.findByText('Бат')).closest('tr')!
    expect(within(row).getByText('Зөвшөөрсөн')).toBeInTheDocument()
    const note = within(row).getByText('Төлбөрөө маргааш төлнө')
    expect(note).toHaveAttribute('title', 'Төлбөрөө маргааш төлнө')
    expect(note.className).toContain('truncate')
    expect(screen.getByRole('columnheader', { name: 'Үр дүн' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: 'Тайлбар' })).toBeInTheDocument()
    const cb = screen.getByText('Дахин залгах', { selector: 'span.inline-flex' })
    expect(cb.className).toMatch(/warning/)
    const skippedRow = screen.getByText('do-not-call').closest('tr')!
    expect(within(skippedRow).getByText('Алгассан')).toBeInTheDocument()
    expect(within(screen.getByLabelText('Төлөвөөр шүүх')).getByRole('option', { name: 'Алгассан' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Төлөвөөр шүүх'), { target: { value: 'skipped' } })
    expect(screen.queryByText('Бат')).toBeNull()
  })

  it('shows the dry-run banner only when paused with dialed calls', async () => {
    setup({ status: 'paused', dryRunLimit: 5, dryRunDialed: 5 })
    expect(await screen.findByTestId('dryrun-banner')).toHaveTextContent('Туршилт дууслаа: 5 дуудлага. Сонсоод бүгдийг эхлүүлнэ үү')
  })
  it('hides the banner for a fresh draft', async () => {
    setup()
    await screen.findByText('Бат')
    expect(screen.queryByTestId('dryrun-banner')).toBeNull()
  })

  it('dry-run dialog posts {dryRunLimit: N} to start', async () => {
    setup()
    const post = vi.spyOn(api, 'post').mockResolvedValue({ campaign: { ...baseCampaign, status: 'running', dryRunLimit: 7 } } as never)
    fireEvent.click(await screen.findByRole('button', { name: /Туршилтаар эхлүүлэх/ }))
    const dialog = screen.getByRole('dialog', { name: 'Туршилтаар эхлүүлэх' })
    fireEvent.change(within(dialog).getByRole('spinbutton'), { target: { value: '7' } })
    expect(within(dialog).getByText(/Эхний 7 дугаарт залгаад автоматаар түр зогсоно/)).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: /Туршилт эхлүүлэх/ }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/campaigns/c1/start', { dryRunLimit: 7 }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Туршилтаар эхлүүлэх' })).toBeNull())
  })

  it('"Бүгдийг эхлүүлэх" posts {dryRunLimit: 0}', async () => {
    setup({ status: 'paused', dryRunDialed: 5, dryRunLimit: 5 })
    const post = vi.spyOn(api, 'post').mockResolvedValue({ campaign: { ...baseCampaign, status: 'running' } } as never)
    fireEvent.click(await screen.findByRole('button', { name: /Бүгдийг эхлүүлэх/ }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/campaigns/c1/start', { dryRunLimit: 0 }))
  })

  it('"Excel татах" fetches export.xlsx with the bearer token and saves a blob', async () => {
    localStorage.setItem('callgo.token', 'tok-123')
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true, status: 200, blob: async () => new Blob(['xlsx']),
      headers: new Headers({ 'Content-Disposition': 'attachment; filename="campaign.xlsx"' }),
    })
    vi.stubGlobal('fetch', fetchMock)
    const createUrl = vi.fn(() => 'blob:x')
    Object.assign(URL, { createObjectURL: createUrl, revokeObjectURL: vi.fn() })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    setup()
    fireEvent.click(await screen.findByRole('button', { name: /Excel татах/ }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/campaigns/c1/export.xlsx')
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer tok-123')
    await waitFor(() => expect(click).toHaveBeenCalled())
    expect(createUrl).toHaveBeenCalled()
  })

  it('edits settings via PUT with schedule/outcomes/concurrency/maxAttempts/script (draft)', async () => {
    setup()
    const put = vi.spyOn(api, 'put').mockResolvedValue({ campaign: baseCampaign } as never)
    fireEvent.click(await screen.findByRole('button', { name: /Тохиргоо засах/ }))
    const dialog = screen.getByRole('dialog', { name: 'Тохиргоо засах' })
    fireEvent.change(within(dialog).getByLabelText(/Зэрэг дуудлага/), { target: { value: '9' } })
    fireEvent.change(within(dialog).getByLabelText('Дуусах цаг'), { target: { value: '20:00' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Ангилал 5 устгах' }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    const [path, body] = put.mock.calls[0] as [string, Record<string, unknown>]
    expect(path).toBe('/campaigns/c1')
    expect(body).toMatchObject({
      concurrency: 9, maxAttempts: 2, script: 'Сайн уу',
      schedule: { timezone: 'Asia/Ulaanbaatar', weekdays: [1, 2, 3, 4, 5], startTime: '09:00', endTime: '20:00', pacePerMinute: 10 },
    })
    expect((body.outcomes as unknown[]).length).toBe(4)
  })

  it('while running, only schedule/concurrency/outcomes are sent', async () => {
    const running = setup({ status: 'running' })
    const put = vi.spyOn(api, 'put').mockResolvedValue({ campaign: running } as never)
    fireEvent.click(await screen.findByRole('button', { name: /Тохиргоо засах/ }))
    const dialog = screen.getByRole('dialog', { name: 'Тохиргоо засах' })
    expect(within(dialog).getByLabelText(/Дахин оролдох/)).toBeDisabled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(Object.keys(body).sort()).toEqual(['concurrency', 'outcomes', 'schedule'])
    expect(screen.queryByRole('button', { name: /Туршилтаар эхлүүлэх/ })).toBeNull()
  })
})
