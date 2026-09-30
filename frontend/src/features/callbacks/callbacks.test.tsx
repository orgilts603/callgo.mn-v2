import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter, RouterProvider, type RouteObject } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// The suite shares a heavily loaded CI box; render-heavy forms need headroom.
vi.setConfig({ testTimeout: 30_000 })
import type { CallbackRequest } from '@/lib/types'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

import { api } from '@/lib/api'
import { routes } from './routes'

function renderRoutes(r: RouteObject[], { path }: { path: string }) {
  const router = createMemoryRouter(r, { initialEntries: [path] })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={qc}><RouterProvider router={router} /></QueryClientProvider>)
}

const mocked = vi.mocked(api)

const pending: CallbackRequest = {
  id: 'cb1', orgId: 'o', phone: '+97699112233', name: 'Болд', note: 'Үнэ асуусан', dueAt: '2026-10-01T09:00:00Z', status: 'pending', attempts: 0, createdAt: '', updatedAt: '',
}
const done: CallbackRequest = { ...pending, id: 'cb2', phone: '+97688001122', name: 'Сараа', status: 'done', attempts: 1, resultCallId: 'call9' }

beforeEach(() => {
  vi.clearAllMocks()
  mocked.get.mockImplementation(async (path: string, query?: Record<string, unknown>) => {
    if (path === '/callbacks') {
      const items = [pending, done].filter((c) => !query?.status || c.status === query.status)
      return { items, total: items.length }
    }
    if (path === '/sip-numbers') return { items: [{ id: 's1', number: '+97670001234', label: 'Үндсэн' }] }
    if (path === '/agent-profiles') return { items: [{ id: 'p1', name: 'Борлуулалт' }] }
    throw new Error(`unexpected GET ${path}`)
  })
})

describe('CallbacksPage', () => {
  it('lists callbacks with status badge, result call link and filters by status', async () => {
    renderRoutes(routes, { path: '/callbacks' })
    expect(await screen.findByText('Болд')).toBeInTheDocument()
    expect(within(screen.getByText('Болд').closest('tr') as HTMLElement).getByText('Хүлээгдэж байна')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Харах' })).toHaveAttribute('href', '/calls/call9')
    // actions only for pending
    expect(screen.getAllByRole('button', { name: /^Цуцлах/ })).toHaveLength(1)

    fireEvent.change(screen.getByLabelText('Төлөвөөр шүүх'), { target: { value: 'done' } })
    await waitFor(() => expect(screen.queryByText('Болд')).not.toBeInTheDocument())
    expect(mocked.get).toHaveBeenCalledWith('/callbacks', { status: 'done', limit: 25, offset: 0 })
  })

  it('creates a callback with a normalized phone and an ISO dueAt', async () => {
    mocked.post.mockResolvedValue({})
    renderRoutes(routes, { path: '/callbacks' })
    await screen.findByText('Болд')
    fireEvent.click(screen.getByRole('button', { name: /Товлох/ }))
    const dialog = await screen.findByRole('dialog')
    const d = within(dialog)
    await d.findByRole('option', { name: 'Үндсэн' })

    fireEvent.change(d.getByLabelText(/^Утасны дугаар/), { target: { value: '99001122' } })
    fireEvent.change(d.getByLabelText(/^Нэр/), { target: { value: 'Дорж' } })
    fireEvent.change(d.getByLabelText(/^Тэмдэглэл/), { target: { value: 'Уулзалт' } })
    fireEvent.change(d.getByLabelText(/^Залгах цаг/), { target: { value: '2026-10-05T14:30' } })
    fireEvent.change(d.getByLabelText(/^SIP дугаар/), { target: { value: 's1' } })
    fireEvent.change(d.getByLabelText(/^Агент профайл/), { target: { value: 'p1' } })
    fireEvent.click(d.getByRole('button', { name: 'Товлох' }))

    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    expect(mocked.post).toHaveBeenCalledWith('/callbacks', {
      phone: '+97699001122', name: 'Дорж', note: 'Уулзалт', dueAt: new Date('2026-10-05T14:30').toISOString(), sipNumberId: 's1', agentProfileId: 'p1',
    })
  })

  it('rejects an invalid phone', async () => {
    renderRoutes(routes, { path: '/callbacks' })
    await screen.findByText('Болд')
    fireEvent.click(screen.getByRole('button', { name: /Товлох/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText(/^Утасны дугаар/), { target: { value: '12' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Товлох' }))
    expect(await within(dialog).findByText(/E\.164/)).toBeInTheDocument()
    expect(mocked.post).not.toHaveBeenCalled()
  })

  it('cancels and reschedules a pending callback', async () => {
    mocked.put.mockResolvedValue({})
    renderRoutes(routes, { path: '/callbacks' })
    await screen.findByText('Болд')

    fireEvent.click(screen.getByRole('button', { name: /^Цаг өөрчлөх/ }))
    const rs = await screen.findByRole('dialog')
    fireEvent.change(within(rs).getByLabelText(/^Шинэ цаг/), { target: { value: '2026-10-07T10:00' } })
    fireEvent.click(within(rs).getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith('/callbacks/cb1', { dueAt: new Date('2026-10-07T10:00').toISOString() }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /^Цуцлах/ }))
    const cd = await screen.findByRole('dialog')
    fireEvent.click(within(cd).getByRole('button', { name: 'Цуцлах' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith('/callbacks/cb1', { status: 'canceled' }))
  })
})
