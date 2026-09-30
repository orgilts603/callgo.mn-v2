import type { ReactNode } from 'react'
import { configure, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { APIKey, AuditEntry, Invitation, Organization, Plan, Subscription, User } from '@/lib/types'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (importOriginal) => {
  const mod = await importOriginal<typeof import('@/lib/api')>()
  return { ...mod, api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }
})

import { api, HttpError } from '@/lib/api'
import { useAuth } from '@/app/auth'
import { testOrg, testUser } from '@/app/testing'
import MembersTab, { memberGuard } from './MembersTab'
import ApiKeysTab from './ApiKeysTab'
import AuditTab, { dayEndISO, dayStartISO } from './AuditTab'
import OrganizationTab from './OrganizationTab'

// The CI box runs many suites in parallel: allow slower renders than the defaults.
configure({ asyncUtilTimeout: 5000 })
vi.setConfig({ testTimeout: 20_000 })

const mocked = vi.mocked(api)
const T = '2026-09-01T00:00:00Z'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={qc}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>)
}

const user = (id: string, role: User['role'], status: User['status'] = 'active', extra: Partial<User> = {}): User => ({
  id, orgId: 'o1', email: `${id}@acme.mn`, name: `User ${id}`, role, status, isPlatformAdmin: false,
  createdAt: T, updatedAt: T, lastLoginAt: T, emailVerifiedAt: T, ...extra,
})
const me = user('u1', 'admin')
const owner = user('u2', 'owner')
const op = user('u3', 'operator')
const disabledOp = user('u4', 'operator', 'disabled')
const invitation: Invitation = { id: 'i1', orgId: 'o1', email: 'new@acme.mn', role: 'operator', expiresAt: '2099-01-01T00:00:00Z', createdAt: T }
const accepted: Invitation = { ...invitation, id: 'i2', email: 'old@acme.mn', acceptedAt: T }

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  useAuth.setState({ token: 't', user: me, org: testOrg, status: 'ready' })
})

// ---------------------------------------------------------------------------
describe('memberGuard', () => {
  it('mirrors the backend rules', () => {
    expect(memberGuard(me, me, 1)).toMatchObject({ canManage: false, reason: expect.stringMatching(/Өөрийн/) })
    expect(memberGuard(owner, me, 1)).toMatchObject({ canManage: false, reason: 'Сүүлийн эзэмшигч' })
    expect(memberGuard(owner, me, 2)).toMatchObject({ canManage: false })
    expect(memberGuard(owner, { ...me, role: 'owner', id: 'x' }, 2)).toEqual({ canManage: true })
    expect(memberGuard(op, me, 1)).toEqual({ canManage: true })
    expect(memberGuard(op, { ...me, role: 'operator' }, 1).canManage).toBe(false)
  })
})

describe('MembersTab', () => {
  beforeEach(() => {
    mocked.get.mockResolvedValue({ items: [me, owner, op, disabledOp], invitations: [invitation, accepted] })
    mocked.post.mockResolvedValue({ invitation })
    mocked.put.mockResolvedValue({ user: op })
    mocked.delete.mockResolvedValue(undefined)
  })

  it('lists members with status and pending invitations only', async () => {
    wrap(<MembersTab />)
    const row = await screen.findByTestId('member-u3')
    expect(within(row).getByText('u3@acme.mn')).toBeInTheDocument()
    expect(within(row).getByText('Идэвхтэй')).toBeInTheDocument()
    expect(within(screen.getByTestId('member-u4')).getByText('Идэвхгүй')).toBeInTheDocument()
    expect(within(screen.getByTestId('member-u1')).getByText('(та)')).toBeInTheDocument()
    expect(mocked.get).toHaveBeenCalledWith('/org/members')
    expect(screen.getByTestId('invitation-i1')).toHaveTextContent('new@acme.mn')
    expect(screen.queryByTestId('invitation-i2')).toBeNull()
  })

  it('invites with email + role', async () => {
    wrap(<MembersTab />)
    fireEvent.click(await screen.findByRole('button', { name: /Гишүүн урих/ }))
    const dialog = screen.getByRole('dialog', { name: 'Гишүүн урих' })
    const role = within(dialog).getByLabelText(/^Эрх/) as HTMLSelectElement
    expect(Array.from(role.options).map((o) => o.value)).toEqual(['admin', 'operator'])
    fireEvent.change(within(dialog).getByLabelText(/^И-мэйл/), { target: { value: ' New@Acme.mn ' } })
    fireEvent.change(role, { target: { value: 'admin' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /Урилга илгээх/ }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/org/invitations', { email: 'new@acme.mn', role: 'admin' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('shows quota errors from the invite endpoint with a billing link', async () => {
    mocked.post.mockRejectedValue(new HttpError(429, 'quota_exceeded', 'max users'))
    wrap(<MembersTab />)
    fireEvent.click(await screen.findByRole('button', { name: /Гишүүн урих/ }))
    const dialog = screen.getByRole('dialog', { name: 'Гишүүн урих' })
    fireEvent.change(within(dialog).getByLabelText(/^И-мэйл/), { target: { value: 'x@acme.mn' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /Урилга илгээх/ }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('хэрэглэгчийн дээд хязгаарт')
    expect(within(dialog).getByRole('link', { name: 'Багц ахиулах' })).toHaveAttribute('href', '/settings/billing')
  })

  it('changes a role and toggles status with the right bodies', async () => {
    wrap(<MembersTab />)
    fireEvent.change(await screen.findByLabelText('u3@acme.mn эрх'), { target: { value: 'admin' } })
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith('/org/members/u3', { role: 'admin' }))
    fireEvent.click(screen.getByRole('button', { name: 'u3@acme.mn идэвхгүй болгох' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith('/org/members/u3', { status: 'disabled' }))
    fireEvent.click(screen.getByRole('button', { name: 'u4@acme.mn идэвхжүүлэх' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith('/org/members/u4', { status: 'active' }))
  })

  it('guards the last owner and yourself in the UI', async () => {
    wrap(<MembersTab />)
    expect(await screen.findByLabelText('u2@acme.mn эрх')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'u2@acme.mn хасах' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'u2@acme.mn идэвхгүй болгох' })).toBeDisabled()
    expect(screen.getByLabelText('u1@acme.mn эрх')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'u1@acme.mn хасах' })).toBeDisabled()
  })

  it('removes a member after confirmation', async () => {
    wrap(<MembersTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'u3@acme.mn хасах' }))
    const dialog = screen.getByRole('dialog', { name: /Гишүүнийг хасах/ })
    expect(mocked.delete).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Хасах' }))
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith('/org/members/u3'))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('resends and cancels pending invitations', async () => {
    wrap(<MembersTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'new@acme.mn урилгыг дахин илгээх' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/org/invitations', { email: 'new@acme.mn', role: 'operator' }))
    fireEvent.click(screen.getByRole('button', { name: 'new@acme.mn урилгыг цуцлах' }))
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith('/org/invitations/i1'))
  })

  it('lets an owner manage a co-owner and assign the owner role', async () => {
    useAuth.setState({ user: { ...me, role: 'owner' } })
    mocked.get.mockResolvedValue({ items: [{ ...me, role: 'owner' }, owner, op], invitations: [] })
    wrap(<MembersTab />)
    const sel = await screen.findByLabelText('u2@acme.mn эрх') as HTMLSelectElement
    expect(sel).not.toBeDisabled()
    expect(Array.from((screen.getByLabelText('u3@acme.mn эрх') as HTMLSelectElement).options).map((o) => o.value)).toContain('owner')
  })

  it('shows a permission note for operators (403)', async () => {
    mocked.get.mockRejectedValue(new HttpError(403, 'forbidden', 'no'))
    useAuth.setState({ user: { ...me, role: 'operator' } })
    wrap(<MembersTab />)
    expect(await screen.findByText('Хандах эрхгүй')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
const key = (id: string, extra: Partial<APIKey> = {}): APIKey => ({
  id, orgId: 'o1', name: `Key ${id}`, prefix: `ab${id}cdef`, scopes: ['calls:read'], createdAt: T, ...extra,
})

describe('ApiKeysTab', () => {
  const plaintext = 'cg_live_abk9cdef_SUPERSECRETVALUE'
  beforeEach(() => {
    mocked.get.mockResolvedValue({ items: [key('k1', { lastUsedAt: T }), key('k2', { revokedAt: T, scopes: ['*'] })] })
    mocked.post.mockResolvedValue({ apiKey: key('k9'), plaintext })
    mocked.delete.mockResolvedValue(undefined)
  })

  it('lists keys with prefix, scopes and last use', async () => {
    wrap(<ApiKeysTab />)
    const row = await screen.findByTestId('api-key-k1')
    expect(row).toHaveTextContent('cg_live_abk1cdef…')
    expect(row).toHaveTextContent('Дуудлага унших')
    expect(within(screen.getByTestId('api-key-k2')).getByText('Цуцлагдсан')).toBeInTheDocument()
    expect(within(screen.getByTestId('api-key-k2')).queryByRole('button')).toBeNull()
  })

  it('creates a key and shows the plaintext exactly once', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    wrap(<ApiKeysTab />)
    fireEvent.click(await screen.findByRole('button', { name: /Түлхүүр үүсгэх/ }))
    let dialog = screen.getByRole('dialog', { name: 'API түлхүүр үүсгэх' })
    fireEvent.change(within(dialog).getByLabelText(/^Нэр/), { target: { value: 'CRM' } })
    fireEvent.click(within(dialog).getByLabelText('Дуудлага хийх'))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Үүсгэх' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/org/api-keys', { name: 'CRM', scopes: ['calls:read', 'calls:write'] }))

    dialog = await screen.findByRole('dialog', { name: 'API түлхүүр үүслээ' })
    expect(within(dialog).getByLabelText('API түлхүүр')).toHaveValue(plaintext)
    expect(within(dialog).getByRole('alert')).toHaveTextContent('зөвхөн нэг удаа')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Хуулах' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(plaintext))

    fireEvent.click(within(dialog).getByRole('button', { name: 'Хадгалсан, хаах' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(screen.queryByDisplayValue(plaintext)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /Түлхүүр үүсгэх/ }))
    expect(screen.getByRole('dialog', { name: 'API түлхүүр үүсгэх' })).toBeInTheDocument()
    expect(screen.queryByDisplayValue(plaintext)).toBeNull()
    expect(document.body.textContent).not.toContain(plaintext)
  })

  it('full access sends only "*" and a name is required', async () => {
    wrap(<ApiKeysTab />)
    fireEvent.click(await screen.findByRole('button', { name: /Түлхүүр үүсгэх/ }))
    const dialog = screen.getByRole('dialog', { name: 'API түлхүүр үүсгэх' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Үүсгэх' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('нэрийг оруулна уу')
    fireEvent.change(within(dialog).getByLabelText(/^Нэр/), { target: { value: 'Admin' } })
    fireEvent.click(within(dialog).getByLabelText('Бүрэн эрх'))
    expect(within(dialog).getByLabelText('Дуудлага унших')).toBeDisabled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Үүсгэх' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/org/api-keys', { name: 'Admin', scopes: ['*'] }))
  })

  it('revokes after confirmation', async () => {
    wrap(<ApiKeysTab />)
    fireEvent.click(await screen.findByRole('button', { name: 'Key k1 цуцлах' }))
    fireEvent.click(within(screen.getByRole('dialog', { name: /API түлхүүр цуцлах/ })).getByRole('button', { name: 'Цуцлах' }))
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith('/org/api-keys/k1'))
  })

  it('explains when the plan lacks the api feature', async () => {
    mocked.get.mockRejectedValue(new HttpError(403, 'feature_unavailable', 'upgrade'))
    wrap(<ApiKeysTab />)
    expect(await screen.findByText('API түлхүүр таны багцад ороогүй')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Багц харах' })).toHaveAttribute('href', '/settings/billing')
  })
})

// ---------------------------------------------------------------------------
const entry = (id: string, extra: Partial<AuditEntry> = {}): AuditEntry => ({
  id, orgId: 'o1', actorId: 'u3', actorEmail: 'u3@acme.mn', action: 'user.invite', targetType: 'invitation', targetId: 'i1', ip: '10.0.0.1', at: T, ...extra,
})

describe('AuditTab', () => {
  let audit: { items: AuditEntry[]; total: number }
  beforeEach(() => {
    audit = { items: [entry('a1', { meta: { email: 'new@acme.mn', role: 'operator' } }), entry('a2')], total: 2 }
    mocked.get.mockImplementation(async (path: string) => {
      if (path === '/org/members') return { items: [me, op], invitations: [] }
      if (path === '/org/audit') return audit
      throw new Error(`unexpected ${path}`)
    })
  })
  const lastAuditQuery = () => mocked.get.mock.calls.filter(([p]) => p === '/org/audit').at(-1)?.[1]

  it('maps filters to query params', async () => {
    wrap(<AuditTab />)
    await screen.findByTestId('audit-a1')
    expect(lastAuditQuery()).toEqual({ limit: 50, offset: 0 })

    await waitFor(() => expect(screen.getByLabelText('Хэрэглэгч').querySelectorAll('option').length).toBe(3))
    fireEvent.change(screen.getByLabelText('Хэрэглэгч'), { target: { value: 'u3' } })
    await waitFor(() => expect(lastAuditQuery()).toEqual({ actorId: 'u3', limit: 50, offset: 0 }))

    fireEvent.change(screen.getByLabelText('Үйлдэл'), { target: { value: ' user.invite ' } })
    await waitFor(() => expect(lastAuditQuery()).toEqual({ actorId: 'u3', action: 'user.invite', limit: 50, offset: 0 }))

    fireEvent.change(screen.getByLabelText('Эхлэх огноо'), { target: { value: '2026-09-01' } })
    fireEvent.change(screen.getByLabelText('Дуусах огноо'), { target: { value: '2026-09-30' } })
    await waitFor(() => expect(lastAuditQuery()).toEqual({
      actorId: 'u3', action: 'user.invite', from: dayStartISO('2026-09-01'), to: dayEndISO('2026-09-30'), limit: 50, offset: 0,
    }))
    expect(new Date(dayEndISO('2026-09-30') as string).getTime() - new Date(dayStartISO('2026-09-30') as string).getTime()).toBe(86_400_000 - 1)

    fireEvent.click(screen.getByRole('button', { name: /Цэвэрлэх/ }))
    await waitFor(() => expect(lastAuditQuery()).toEqual({ limit: 50, offset: 0 }))
  })

  it('paginates by offset', async () => {
    audit = { ...audit, total: 120 }
    wrap(<AuditTab />)
    await screen.findByTestId('audit-a1')
    fireEvent.click(screen.getByRole('button', { name: 'Дараах' }))
    await waitFor(() => expect(lastAuditQuery()).toEqual({ limit: 50, offset: 50 }))
    expect(screen.getByText('51–100 / 120')).toBeInTheDocument()
  })

  it('expands meta JSON', async () => {
    wrap(<AuditTab />)
    const row = await screen.findByTestId('audit-a1')
    expect(within(screen.getByTestId('audit-a2')).queryByRole('button')).toBeNull()
    fireEvent.click(within(row).getByRole('button', { name: 'Дэлгэрэнгүй харах' }))
    expect(screen.getByTestId('audit-meta')).toHaveTextContent('"email": "new@acme.mn"')
    fireEvent.click(within(row).getByRole('button', { name: 'Дэлгэрэнгүйг хураах' }))
    expect(screen.queryByTestId('audit-meta')).toBeNull()
  })
})

// ---------------------------------------------------------------------------
describe('OrganizationTab', () => {
  const org: Organization = { ...testOrg, name: 'Acme', timezone: 'Asia/Ulaanbaatar' }
  const plan = { code: 'pro', name: 'Pro', includedMinutes: 3000 } as Plan
  const subscription = { status: 'active', currentPeriodStart: T, currentPeriodEnd: '2026-10-01T00:00:00Z' } as Subscription

  it('saves name and timezone changes and shows the plan', async () => {
    mocked.get.mockResolvedValue({ org, plan, subscription })
    mocked.put.mockResolvedValue({ org: { ...org, name: 'Acme LLC', timezone: 'Asia/Hovd' } })
    wrap(<OrganizationTab />)
    expect(await screen.findByTestId('plan-badge')).toHaveTextContent('Pro')
    expect(screen.getByRole('link', { name: /Багц, төлбөрийн тохиргоо/ })).toHaveAttribute('href', '/settings/billing')
    expect(screen.queryByRole('alert')).toBeNull()

    const save = screen.getByRole('button', { name: 'Хадгалах' })
    expect(save).toBeDisabled()
    fireEvent.change(screen.getByLabelText(/^Байгууллагын нэр/), { target: { value: 'Acme LLC' } })
    fireEvent.change(screen.getByLabelText(/^Цагийн бүс/), { target: { value: 'Asia/Hovd' } })
    fireEvent.click(save)
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith('/org', { name: 'Acme LLC', timezone: 'Asia/Hovd' }))
    await waitFor(() => expect(useAuth.getState().org?.name).toBe('Acme LLC'))
  })

  it('warns when the org is suspended and links to billing', async () => {
    mocked.get.mockResolvedValue({ org: { ...org, status: 'suspended' }, plan, subscription })
    wrap(<OrganizationTab />)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('түр хаагдсан')
    expect(within(alert).getByRole('link', { name: /Төлбөр төлөх/ })).toHaveAttribute('href', '/settings/billing')
  })

  it('is read-only for operators', async () => {
    useAuth.setState({ user: { ...testUser, role: 'operator' } })
    mocked.get.mockResolvedValue({ org, plan: null, subscription: null })
    wrap(<OrganizationTab />)
    expect(await screen.findByLabelText(/^Байгууллагын нэр/)).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Хадгалах' })).toBeNull()
    expect(screen.getByTestId('plan-badge')).toHaveTextContent('trial')
  })
})
