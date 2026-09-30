import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import BillingPage from './BillingPage'
import { routes } from './routes'
import { makeInvoice, makeSubResponse, plans } from './fixtures'
import { fakeLive, makeClient, renderWith } from './testing'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))
const get = vi.mocked(api.get)
const original = useLive.getState()

beforeEach(() => {
  vi.clearAllMocks()
  get.mockImplementation(async (path: string) => {
    if (path === '/billing/plans') return { items: plans }
    if (path === '/billing/subscription') return makeSubResponse()
    if (path === '/billing/invoices') return { items: [makeInvoice()] }
    if (path === '/billing/usage') return { summary: makeSubResponse().usage, daily: [] }
    throw new Error(`unexpected GET ${path}`)
  })
})
afterEach(() => { useLive.setState(original) })

describe('BillingPage', () => {
  it('switches between Багц / Хэрэглээ / Нэхэмжлэх via ?view=', async () => {
    fakeLive()
    renderWith(<BillingPage />, { path: '/settings/billing' })
    expect(await screen.findByTestId('plan-starter')).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Багц' })).toHaveAttribute('aria-selected', 'true')

    fireEvent.click(screen.getByRole('tab', { name: 'Хэрэглээ' }))
    expect(await screen.findByText('Ярианы минут')).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/settings/billing?view=usage')

    fireEvent.click(screen.getByRole('tab', { name: 'Нэхэмжлэх' }))
    expect(await screen.findByTestId('invoice-row')).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/settings/billing?view=invoices')
  })

  it('opens the requested view from the URL and refreshes on billing.updated', async () => {
    const live = fakeLive()
    renderWith(<BillingPage />, { path: '/settings/billing?view=invoices' })
    expect(await screen.findByTestId('invoice-row')).toBeInTheDocument()
    const before = get.mock.calls.filter(([p]) => p === '/billing/invoices').length
    live.emit('billing.updated', { subscription: makeSubResponse().subscription })
    await waitFor(() => expect(get.mock.calls.filter(([p]) => p === '/billing/invoices').length).toBeGreaterThan(before))
  })

  it('/billing redirects to /settings/billing keeping ?view=', async () => {
    fakeLive()
    render(
      <QueryClientProvider client={makeClient()}>
        <MemoryRouter initialEntries={['/billing?view=usage']}>
          <Routes>
            {routes.map((r) => <Route key={r.path} path={r.path} element={r.element} />)}
            <Route path="/settings/:tab" element={<BillingPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    expect(await screen.findByText('Ярианы минут')).toBeInTheDocument()
  })
})

describe('Settings → Төлбөр tab', () => {
  it('renders BillingPage inside SettingsPage at /settings/billing', async () => {
    fakeLive()
    const { default: SettingsPage } = await import('@/features/settings/SettingsPage')
    render(
      <QueryClientProvider client={makeClient()}>
        <MemoryRouter initialEntries={['/settings/billing']}>
          <Routes><Route path="/settings/:tab" element={<SettingsPage />} /></Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    expect(screen.getByRole('link', { name: /Төлбөр/ })).toHaveAttribute('aria-current', 'page')
    expect(await screen.findByTestId('plan-growth')).toBeInTheDocument()
  })
})
