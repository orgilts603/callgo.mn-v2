import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { onTestFinished } from 'vitest'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import { InvoicesTable } from './InvoicesTable'
import { makeInvoice, makePayment } from './fixtures'
import { fakeLive, renderWith } from './testing'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))
const get = vi.mocked(api.get)
const original = useLive.getState()

const invoices = [
  makeInvoice({ id: 'inv1', number: 'INV-0003', status: 'open', totalMnt: 979_000 }),
  makeInvoice({ id: 'inv2', number: 'INV-0002', status: 'paid', totalMnt: 319_000, paidAt: '2026-08-03T00:00:00Z' }),
  makeInvoice({ id: 'inv3', number: 'INV-0001', status: 'void', totalMnt: 1_290_000 }),
]

function mockGet() {
  get.mockImplementation(async (path: string) => {
    if (path === '/billing/invoices') return { items: invoices }
    if (path === '/billing/invoices/inv2') return { invoice: { ...invoices[1], lines: [
      { description: 'Starter багц', quantity: 1, unitMnt: 290_000, amountMnt: 290_000 },
    ], subtotalMnt: 290_000, vatMnt: 29_000 }, payments: [makePayment({ id: 'p9', invoiceId: 'inv2', status: 'paid', amountMnt: 319_000 })] }
    throw new Error(`unexpected GET ${path}`)
  })
}

describe('InvoicesTable', { timeout: 20_000 }, () => {
  beforeEach(() => { vi.clearAllMocks(); fakeLive(); localStorage.setItem('callgo.token', 'tok-123') })
  afterEach(() => { useLive.setState(original); vi.unstubAllGlobals(); vi.restoreAllMocks(); localStorage.clear() })

  it('lists invoices with status badges and pay only for open ones', async () => {
    mockGet()
    renderWith(<InvoicesTable />)
    const rows = await screen.findAllByTestId('invoice-row')
    expect(rows).toHaveLength(3)
    expect(within(rows[0]).getByText('Төлөгдөөгүй')).toBeInTheDocument()
    expect(within(rows[0]).getByText('979 000 ₮')).toBeInTheDocument()
    expect(within(rows[0]).getByText('2026-09-08')).toBeInTheDocument()
    expect(within(rows[1]).getByText('Төлөгдсөн')).toBeInTheDocument()
    expect(within(rows[2]).getByText('Хүчингүй')).toBeInTheDocument()
    expect(within(rows[2]).getByText('1 290 000 ₮')).toBeInTheDocument()
    expect(within(rows[0]).getByRole('button', { name: 'Төлөх' })).toBeInTheDocument()
    expect(within(rows[1]).queryByRole('button', { name: 'Төлөх' })).not.toBeInTheDocument()
    expect(within(rows[2]).queryByRole('button', { name: 'Төлөх' })).not.toBeInTheDocument()

    fireEvent.click(within(rows[0]).getByRole('button', { name: 'Төлөх' }))
    expect(await screen.findByText('Төлбөр төлөх')).toBeInTheDocument()
    expect(screen.getByTestId('pay-amount')).toHaveTextContent('979 000 ₮')
  })

  it('"Харах" fetches the PDF with the bearer token and opens it via an object URL', async () => {
    mockGet()
    const fetchMock = vi.fn(async () => ({ ok: true, status: 200, statusText: 'OK', blob: async () => new Blob(['%PDF-1.4'], { type: 'application/pdf' }) }))
    vi.stubGlobal('fetch', fetchMock)
    const tab = { opener: {} as unknown, location: { href: '' }, close: vi.fn() }
    const open = vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    const createUrl = vi.fn((b: Blob) => (b.type === 'application/pdf' ? 'blob:pdf-1' : 'blob:wrong'))
    const saved = { create: URL.createObjectURL, revoke: URL.revokeObjectURL }
    URL.createObjectURL = createUrl
    URL.revokeObjectURL = vi.fn()
    onTestFinished(() => { URL.createObjectURL = saved.create; URL.revokeObjectURL = saved.revoke })

    renderWith(<InvoicesTable />)
    const rows = await screen.findAllByTestId('invoice-row')
    fireEvent.click(within(rows[1]).getByRole('button', { name: 'Харах' }))

    await waitFor(() => expect(tab.location.href).toBe('blob:pdf-1'))
    expect(open).toHaveBeenCalledWith('', '_blank')
    expect(fetchMock).toHaveBeenCalledWith('/api/billing/invoices/inv2/pdf', { headers: { Authorization: 'Bearer tok-123' } })
    expect(createUrl).toHaveBeenCalled()
    expect(tab.opener).toBeNull()
    // clicking the action must not open the detail drawer
    expect(get).not.toHaveBeenCalledWith('/billing/invoices/inv2')
  })

  it('closes the tab and toasts when the PDF request fails', async () => {
    mockGet()
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 500, statusText: 'Internal Server Error', blob: async () => new Blob([]) })))
    const tab = { opener: null, location: { href: '' }, close: vi.fn() }
    vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    renderWith(<InvoicesTable />)
    fireEvent.click(within((await screen.findAllByTestId('invoice-row'))[0]).getByRole('button', { name: 'Харах' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Internal Server Error'))
    expect(tab.close).toHaveBeenCalled()
  })

  it('opens a detail drawer with lines, totals and payments', async () => {
    mockGet()
    renderWith(<InvoicesTable />)
    fireEvent.click((await screen.findAllByTestId('invoice-row'))[1])
    await waitFor(() => expect(get).toHaveBeenCalledWith('/billing/invoices/inv2'))
    const drawer = (await screen.findByText('Нэхэмжлэх INV-0002')).closest('aside')!
    expect(await within(drawer).findByText('Starter багц')).toBeInTheDocument()
    expect(within(drawer).getByText('НӨАТ (10%)')).toBeInTheDocument()
    expect(within(drawer).getByText('29 000 ₮')).toBeInTheDocument()
    expect(within(drawer).getAllByText('319 000 ₮').length).toBeGreaterThanOrEqual(2)
    expect(within(drawer).getByText('qpay')).toBeInTheDocument()
    expect(within(drawer).queryByRole('button', { name: 'Төлөх' })).not.toBeInTheDocument()
  })

  it('shows an empty state', async () => {
    get.mockResolvedValue({ items: [] })
    renderWith(<InvoicesTable />)
    expect(await screen.findByText('Нэхэмжлэх алга')).toBeInTheDocument()
  })
})
