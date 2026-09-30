import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import { PlansGrid } from './PlansGrid'
import { makeInvoice, makeSubResponse, plans } from './fixtures'
import { fakeLive, renderWith } from './testing'
import type { SubscriptionResponse } from './hooks'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

const get = vi.mocked(api.get)
const post = vi.mocked(api.post)
const original = useLive.getState()

function mockGet(sub: SubscriptionResponse, invoices = [] as ReturnType<typeof makeInvoice>[]) {
  get.mockImplementation(async (path: string) => {
    if (path === '/billing/plans') return { items: plans }
    if (path === '/billing/subscription') return sub
    if (path === '/billing/invoices') return { items: invoices }
    throw new Error(`unexpected GET ${path}`)
  })
}

describe('PlansGrid', { timeout: 20_000 }, () => {
  beforeEach(() => { vi.clearAllMocks(); fakeLive() })
  afterEach(() => { useLive.setState(original) })

  it('renders public plans with prices and highlights the current plan', async () => {
    mockGet(makeSubResponse({ plan: 'starter' }))
    renderWith(<PlansGrid />)
    const starter = await screen.findByTestId('plan-starter')
    await waitFor(() => expect(starter).toHaveAttribute('aria-current', 'true'))
    expect(within(starter).getByText('Одоогийн багц')).toBeInTheDocument()
    expect(within(starter).getByText('290 000 ₮')).toBeInTheDocument()
    expect(within(starter).getByRole('button', { name: 'Идэвхтэй' })).toBeDisabled()
    expect(within(screen.getByTestId('plan-growth')).getByText('890 000 ₮')).toBeInTheDocument()
    expect(within(screen.getByTestId('plan-growth')).getByText('Webhook интеграц')).toBeInTheDocument()
    expect(screen.queryByTestId('plan-internal')).not.toBeInTheDocument()
    const contact = within(screen.getByTestId('plan-enterprise')).getByRole('link', { name: /Холбогдох/ })
    expect(contact.getAttribute('href')).toMatch(/^mailto:sales@callgo\.mn/)
    expect(screen.getByText('Тохиролцоно')).toBeInTheDocument()
  })

  it('selecting a plan confirms, posts planCode and opens the pay dialog for the returned invoice', async () => {
    mockGet(makeSubResponse({ plan: 'starter' }))
    const invoice = makeInvoice({ totalMnt: 979_000 })
    post.mockResolvedValue({ subscription: makeSubResponse({ plan: 'growth' }).subscription, invoice })
    renderWith(<PlansGrid />)

    fireEvent.click(await screen.findByRole('button', { name: 'Growth багц сонгох' }))
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('"Growth" багц руу шилжих үү?')
    expect(dialog).toHaveTextContent('890 000 ₮')
    expect(post).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Баталгаажуулах' }))

    await waitFor(() => expect(post).toHaveBeenCalledWith('/billing/subscription', { planCode: 'growth' }))
    expect(await screen.findByText('Төлбөр төлөх')).toBeInTheDocument()
    expect(screen.getByTestId('pay-amount')).toHaveTextContent('979 000 ₮')
    expect(screen.getByRole('radio', { name: 'QPay' })).toBeChecked()
    expect(toast.success).toHaveBeenCalled()
  })

  it('does not open the pay dialog when no invoice is returned (downgrade)', async () => {
    mockGet(makeSubResponse({ plan: 'growth' }))
    post.mockResolvedValue({ subscription: makeSubResponse({ plan: 'starter' }).subscription })
    renderWith(<PlansGrid />)
    fireEvent.click(await screen.findByRole('button', { name: 'Starter багц сонгох' }))
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Баталгаажуулах' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/billing/subscription', { planCode: 'starter' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(screen.queryByText('Төлбөр төлөх')).not.toBeInTheDocument()
  })

  it('shows the trial countdown chip and the past-due warning with a pay shortcut', async () => {
    const trialEndsAt = new Date(Date.now() + 2 * 86_400_000 - 60_000).toISOString()
    mockGet(makeSubResponse({ plan: 'trial', sub: { status: 'trialing', trialEndsAt } }))
    const { unmount } = renderWith(<PlansGrid />)
    expect(await screen.findByTestId('trial-chip')).toHaveTextContent('Туршилт дуусахад 2 хоног')
    unmount()

    mockGet(makeSubResponse({ plan: 'growth', sub: { status: 'past_due' } }), [makeInvoice({ totalMnt: 979_000 })])
    renderWith(<PlansGrid />)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Төлбөрийн хугацаа хэтэрсэн')
    fireEvent.click(await within(alert).findByRole('button', { name: /Төлөх · 979 000 ₮/ }))
    expect(await screen.findByText('Төлбөр төлөх')).toBeInTheDocument()
  })
})
