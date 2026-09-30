import { configure, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/lib/api'
import type { Invoice, Organization, Plan, Subscription } from '@/lib/types'
import { renderRoutes, resetAuth, signIn, testUser } from '@/app/testing'
import { useAuth } from '@/app/auth'
import AdminPage from './AdminPage'
import { buildSubscriptionBody, parseCustomLimits } from './OrgDrawer'

// jsdom + recharts/wavesurfer are slow when the CI box is busy.
vi.setConfig({ testTimeout: 30_000 })
configure({ asyncUtilTimeout: 10_000 })

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const iso = '2026-09-01T00:00:00Z'
const org = (id: string, name: string, status: Organization['status'] = 'active'): Organization => ({
  id, name, slug: name.toLowerCase(), planCode: 'starter', status, timezone: 'Asia/Ulaanbaatar', createdAt: iso, updatedAt: iso,
})
const sub = (orgId: string): Subscription => ({
  id: `s-${orgId}`, orgId, planCode: 'starter', status: 'active', currentPeriodStart: '2026-09-01T00:00:00Z', currentPeriodEnd: '2026-10-01T00:00:00Z', createdAt: iso, updatedAt: iso,
})
const usage = { orgId: 'o1', periodStart: iso, periodEnd: iso, calls: 10, minutes: 120.5, includedMinutes: 500, overageMinutes: 0, overageMnt: 0, llmTokens: 0, sttSeconds: 0, ttsChars: 0, sms: 0, costMnt: 5000 }
const plans = ['starter', 'pro'].map((code) => ({ code, name: code === 'pro' ? 'Pro' : 'Starter', monthlyMnt: 1, includedMinutes: 1, overageMntPerMin: 1, maxConcurrentCalls: 1, maxAgentProfiles: 1, maxUsers: 1, maxKnowledgeMb: 1, maxSipNumbers: 1, features: [], trialDays: 0, public: true })) as Plan[]
const invoices: Invoice[] = [
  { id: 'inv1', orgId: 'o1', number: 'INV-001', periodStart: iso, periodEnd: '2026-10-01T00:00:00Z', lines: [], subtotalMnt: 90000, vatMnt: 10000, totalMnt: 100000, status: 'open', dueAt: iso, createdAt: iso },
  { id: 'inv2', orgId: 'o1', number: 'INV-000', periodStart: iso, periodEnd: iso, lines: [], subtotalMnt: 1, vatMnt: 0, totalMnt: 1, status: 'paid', dueAt: iso, paidAt: iso, createdAt: iso },
]

function mockApi() {
  const get = vi.spyOn(api, 'get').mockImplementation(async (path: string) => {
    if (path === '/admin/stats') return { orgs: 42, activeSubscriptions: 30, mrrMnt: 3_500_000, callsToday: 812, minutesToday: 1500.5 }
    if (path === '/admin/orgs') return { items: [{ org: org('o1', 'Гоё ХХК'), subscription: sub('o1'), usage, users: 7 }, { org: org('o2', 'Хаагдсан', 'suspended'), users: 1 }], total: 2 }
    if (path === '/admin/orgs/o1') return { org: org('o1', 'Гоё ХХК'), subscription: sub('o1'), plan: plans[0], usage, users: 7, invoices }
    if (path === '/billing/plans') return { items: plans }
    throw new Error(`unexpected GET ${path}`)
  })
  const put = vi.spyOn(api, 'put').mockResolvedValue({ subscription: sub('o1'), org: org('o1', 'Гоё ХХК') })
  const post = vi.spyOn(api, 'post').mockResolvedValue({ invoice: invoices[0] })
  return { get, put, post }
}

const renderAdmin = () => renderRoutes([{ path: '/admin', element: <AdminPage /> }, { path: '/', element: <div>Нүүр</div> }], { path: '/admin' })

describe('AdminPage', () => {
  beforeEach(() => { resetAuth(); signIn(); useAuth.setState({ user: { ...testUser, isPlatformAdmin: true } }) })
  afterEach(() => { vi.restoreAllMocks() })

  it('redirects non platform admins to the dashboard', async () => {
    useAuth.setState({ user: { ...testUser, isPlatformAdmin: false } })
    const { get } = mockApi()
    renderAdmin()
    expect(await screen.findByText('Нүүр')).toBeInTheDocument()
    expect(get).not.toHaveBeenCalled()
  })

  it('renders stats and the orgs table with search', async () => {
    const { get } = mockApi()
    renderAdmin()
    const stats = await screen.findByTestId('admin-stats')
    await waitFor(() => expect(within(stats).getByText('42')).toBeInTheDocument())
    expect(within(stats).getByText('3 500 000 ₮')).toBeInTheDocument()
    expect(within(stats).getByText('812')).toBeInTheDocument()

    const table = await screen.findByRole('table', { name: 'Байгууллагууд' })
    expect(within(table).getByText('Гоё ХХК')).toBeInTheDocument()
    expect(within(table).getByText('120.5 / 500')).toBeInTheDocument()
    expect(within(table).getByText('Түр хаагдсан')).toBeInTheDocument()
    expect(get).toHaveBeenCalledWith('/admin/orgs', { q: undefined, limit: 20, offset: 0 })

    fireEvent.change(screen.getByLabelText('Байгууллага хайх'), { target: { value: 'гоё' } })
    await waitFor(() => expect(get).toHaveBeenCalledWith('/admin/orgs', { q: 'гоё', limit: 20, offset: 0 }))
  })

  it('opens the org drawer and saves the subscription with the form body', async () => {
    const { put } = mockApi()
    renderAdmin()
    fireEvent.click(await screen.findByRole('button', { name: 'Гоё ХХК' }))
    const form = await screen.findByRole('form', { name: 'Захиалгын форм' })
    await waitFor(() => expect(within(form).getByLabelText('Багц')).toHaveValue('starter'))
    fireEvent.change(within(form).getByLabelText('Багц'), { target: { value: 'pro' } })
    fireEvent.change(within(form).getByLabelText('Захиалгын төлөв'), { target: { value: 'past_due' } })
    fireEvent.change(within(form).getByLabelText('Хугацаа дуусах өдөр'), { target: { value: '2026-11-15' } })
    fireEvent.click(within(form).getByRole('button', { name: /Нарийвчилсан/ }))
    fireEvent.change(within(form).getByLabelText('Custom limits JSON'), { target: { value: '{"includedMinutes": 5000}' } })
    fireEvent.click(within(form).getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    const [path, body] = put.mock.calls[0] as [string, Record<string, unknown>]
    expect(path).toBe('/admin/orgs/o1/subscription')
    expect(body).toMatchObject({ planCode: 'pro', status: 'past_due', customLimits: { includedMinutes: 5000 } })
    expect(new Date(body.currentPeriodEnd as string).getFullYear()).toBe(2026)
  })

  it('rejects invalid custom limits JSON without calling the api', async () => {
    const { put } = mockApi()
    renderAdmin()
    fireEvent.click(await screen.findByRole('button', { name: 'Гоё ХХК' }))
    const form = await screen.findByRole('form', { name: 'Захиалгын форм' })
    await waitFor(() => expect(within(form).getByLabelText('Багц')).toHaveValue('starter'))
    fireEvent.click(within(form).getByRole('button', { name: /Нарийвчилсан/ }))
    fireEvent.change(within(form).getByLabelText('Custom limits JSON'), { target: { value: '{oops' } })
    fireEvent.click(within(form).getByRole('button', { name: 'Хадгалах' }))
    expect(await within(form).findByText('JSON буруу байна')).toBeInTheDocument()
    expect(put).not.toHaveBeenCalled()
  })

  it('suspends an org after confirmation', async () => {
    const { put } = mockApi()
    renderAdmin()
    fireEvent.click(await screen.findByRole('button', { name: 'Гоё ХХК' }))
    fireEvent.click(await screen.findByRole('button', { name: /Түр хаах/ }))
    expect(put).not.toHaveBeenCalled()
    const dialog = screen.getByRole('dialog', { name: 'Байгууллагыг түр хаах уу?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Түр хаах' }))
    await waitFor(() => expect(put).toHaveBeenCalledWith('/admin/orgs/o1', { status: 'suspended' }))
  })

  it('marks an open invoice as paid with a note', async () => {
    const { post } = mockApi()
    renderAdmin()
    fireEvent.click(await screen.findByRole('button', { name: 'Гоё ХХК' }))
    const table = await screen.findByRole('table', { name: 'Нэхэмжлэхүүд' })
    expect(within(table).getAllByRole('button', { name: 'Төлсөн гэж тэмдэглэх' })).toHaveLength(1) // paid invoice has no action
    fireEvent.click(within(table).getByRole('button', { name: 'Төлсөн гэж тэмдэглэх' }))
    const dialog = screen.getByRole('dialog', { name: 'Төлсөн гэж тэмдэглэх' })
    fireEvent.change(within(dialog).getByLabelText('Тэмдэглэл'), { target: { value: ' Дансаар шилжүүлсэн ' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Тэмдэглэх' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/admin/invoices/inv1/mark-paid', { note: 'Дансаар шилжүүлсэн' }))
  })

  it('builds subscription bodies', () => {
    expect(parseCustomLimits('')).toEqual({})
    expect(parseCustomLimits('[1]').error).toBeTruthy()
    const same = buildSubscriptionBody({ planCode: 'pro', status: 'active', periodEnd: '2026-10-01', customLimits: '' }, '2026-10-01')
    expect(same.body).toEqual({ planCode: 'pro', status: 'active' })
  })
})
