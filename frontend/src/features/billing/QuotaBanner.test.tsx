import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import { useAuth } from '@/app/auth'
import { testOrg, testUser } from '@/app/testing'
import { QuotaBanner, quotaBanners } from './QuotaBanner'
import { makeSubResponse } from './fixtures'
import { fakeLive, renderWith } from './testing'
import type { SubscriptionResponse } from './hooks'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))
const get = vi.mocked(api.get)
const original = useLive.getState()
const NOW = Date.parse('2026-09-30T12:00:00Z')

function signIn(status: 'active' | 'suspended' = 'active') {
  useAuth.setState({ token: 't', user: testUser, org: { ...testOrg, status }, status: 'ready' })
}
function mockSub(sub: SubscriptionResponse) {
  get.mockImplementation(async (path: string) => {
    if (path === '/billing/subscription') return sub
    throw new Error(`unexpected GET ${path}`)
  })
}

describe('quotaBanners', () => {
  it('returns nothing for a healthy subscription', () => {
    expect(quotaBanners(makeSubResponse({ usage: { minutes: 500 } }), 'active', NOW)).toEqual([])
  })
  it('warns amber at ≥ 80 % and rose at ≥ 100 % with an upgrade link', () => {
    expect(quotaBanners(makeSubResponse({ usage: { minutes: 800 } }), 'active', NOW)).toEqual([expect.objectContaining({ kind: 'minutes_warning', tone: 'warning' })])
    const [b] = quotaBanners(makeSubResponse({ usage: { minutes: 1000 } }), 'active', NOW)
    expect(b).toMatchObject({ kind: 'minutes_exceeded', tone: 'danger', action: { label: 'Багц ахиулах', to: '/settings/billing' } })
  })
  it('flags a trial ending within 3 days only', () => {
    const at = (days: number) => new Date(NOW + days * 86_400_000).toISOString()
    const trial = (d: number) => makeSubResponse({ plan: 'trial', sub: { status: 'trialing', trialEndsAt: at(d) }, usage: { minutes: 0, includedMinutes: 100 } })
    expect(quotaBanners(trial(5), 'active', NOW)).toEqual([])
    expect(quotaBanners(trial(3), 'active', NOW)).toEqual([expect.objectContaining({ kind: 'trial_ending', message: 'Туршилтын хугацаа 3 хоногийн дараа дуусна.' })])
    expect(quotaBanners(trial(-1), 'active', NOW)[0].message).toBe('Туршилтын хугацаа дууссан.')
  })
  it('reports past_due and suspended (suspended supersedes past_due)', () => {
    expect(quotaBanners(makeSubResponse({ sub: { status: 'past_due' } }), 'active', NOW).map((b) => b.kind)).toEqual(['past_due'])
    expect(quotaBanners(makeSubResponse({ sub: { status: 'past_due' } }), 'suspended', NOW).map((b) => b.kind)).toEqual(['suspended'])
    expect(quotaBanners(undefined, 'suspended', NOW)).toEqual([expect.objectContaining({ kind: 'suspended', tone: 'danger' })])
  })
})

describe('QuotaBanner', () => {
  beforeEach(() => { vi.clearAllMocks(); fakeLive() })
  afterEach(() => {
    useLive.setState(original)
    useAuth.setState({ token: null, user: null, org: null, status: 'idle' })
  })

  it('renders nothing when all is well', async () => {
    signIn()
    mockSub(makeSubResponse({ usage: { minutes: 10 } }))
    renderWith(<QuotaBanner />)
    await waitFor(() => expect(get).toHaveBeenCalledWith('/billing/subscription'))
    expect(screen.queryByTestId('quota-banner')).not.toBeInTheDocument()
  })

  it('shows an amber, dismissible banner at 85 %', async () => {
    signIn()
    mockSub(makeSubResponse({ usage: { minutes: 850 } }))
    renderWith(<QuotaBanner />)
    const banner = await screen.findByRole('status')
    expect(banner).toHaveAttribute('data-tone', 'warning')
    expect(banner).toHaveTextContent('Багцын минутын 85% ашиглагдлаа (850 / 1 000 мин).')
    fireEvent.click(screen.getByRole('button', { name: 'Хаах' }))
    expect(screen.queryByTestId('quota-banner')).not.toBeInTheDocument()
  })

  it('shows a rose banner with an upgrade link when minutes are exhausted', async () => {
    signIn()
    mockSub(makeSubResponse({ usage: { minutes: 1050 } }))
    renderWith(<QuotaBanner />)
    const banner = await screen.findByRole('alert')
    expect(banner).toHaveAttribute('data-tone', 'danger')
    fireEvent.click(screen.getByRole('link', { name: 'Багц ахиулах' }))
    expect(screen.getByTestId('location')).toHaveTextContent('/settings/billing')
  })

  it('shows past_due, suspended and trial-ending states', async () => {
    signIn('suspended')
    mockSub(makeSubResponse({ sub: { status: 'past_due' } }))
    const { unmount } = renderWith(<QuotaBanner />)
    expect(await screen.findByRole('alert')).toHaveTextContent('байгууллагын эрх түр хаагдсан')
    expect(screen.getAllByRole('alert')).toHaveLength(1)
    unmount()

    signIn()
    mockSub(makeSubResponse({ sub: { status: 'past_due' } }))
    const second = renderWith(<QuotaBanner />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Төлбөрийн хугацаа хэтэрсэн')
    fireEvent.click(screen.getByRole('link', { name: 'Нэхэмжлэх төлөх' }))
    expect(screen.getByTestId('location')).toHaveTextContent('/settings/billing?view=invoices')
    second.unmount()

    mockSub(makeSubResponse({ plan: 'trial', sub: { status: 'trialing', trialEndsAt: new Date(Date.now() + 86_400_000).toISOString() }, usage: { minutes: 0, includedMinutes: 100 } }))
    renderWith(<QuotaBanner />)
    expect(await screen.findByRole('status')).toHaveTextContent('Туршилтын хугацаа 1 хоногийн дараа дуусна.')
  })

  it('toasts on quota.warning events and refreshes the subscription', async () => {
    signIn()
    const live = fakeLive()
    mockSub(makeSubResponse({ usage: { minutes: 10 } }))
    renderWith(<QuotaBanner />)
    await waitFor(() => expect(get).toHaveBeenCalledTimes(1))
    act(() => live.emit('quota.warning', { used: 800, limit: 1000, percent: 80 }))
    expect(toast.warning).toHaveBeenCalledWith('Багцын минутын 80% ашиглагдлаа (800 / 1 000 мин)')
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    act(() => live.emit('quota.warning', { used: 1000, limit: 1000, percent: 100 }))
    expect(toast.error).toHaveBeenCalledWith('Багцын минут дууслаа (1 000 / 1 000 мин)')
  })

  it('does not query billing when signed out', () => {
    renderWith(<QuotaBanner />)
    expect(get).not.toHaveBeenCalled()
  })
})
