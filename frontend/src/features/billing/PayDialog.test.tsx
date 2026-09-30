import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type { Payment } from '@/lib/types'
import { PayDialog } from './PayDialog'
import { billingKeys } from './hooks'
import { makeInvoice, makePayment } from './fixtures'
import { fakeLive, makeClient, renderWith } from './testing'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

const get = vi.mocked(api.get)
const post = vi.mocked(api.post)
const original = useLive.getState()
const paymentGets = () => get.mock.calls.filter(([p]) => p === '/billing/payments/pay1').length

describe('PayDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })
  afterEach(() => {
    vi.useRealTimers()
    useLive.setState(original)
  })

  it('creates a QPay payment, renders the QR + bank links, polls every 3 s until paid', async () => {
    fakeLive()
    const expiresAt = new Date(Date.now() + 5 * 60_000).toISOString()
    const pending = makePayment({ expiresAt })
    post.mockResolvedValue({ payment: pending })
    const responses: Payment[] = [pending, pending, { ...pending, status: 'paid', paidAt: new Date().toISOString() }]
    get.mockImplementation(async (path: string) => {
      if (path === '/billing/payments/pay1') return { payment: responses.shift() ?? responses[0] }
      return {}
    })
    const client = makeClient()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const onPaid = vi.fn()
    renderWith(<PayDialog invoice={makeInvoice()} onClose={vi.fn()} onPaid={onPaid} />, { client })

    expect(screen.getByTestId('pay-amount')).toHaveTextContent('979 000 ₮')
    fireEvent.click(screen.getByRole('button', { name: 'QR код үүсгэх' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/billing/invoices/inv1/pay', { provider: 'qpay' }))

    const qr = await screen.findByRole('img', { name: 'Төлбөрийн QR код' })
    expect(qr.tagName.toLowerCase()).toBe('svg')
    expect(screen.getByRole('link', { name: /Khan bank/ })).toHaveAttribute('href', 'khanbank://q?qPay_QRcode=abc')
    expect(screen.getByRole('link', { name: /Golomt bank/ })).toBeInTheDocument()
    expect(screen.getByTestId('payment-countdown')).toHaveTextContent(/[45]:\d\d/)
    expect(paymentGets()).toBe(0)

    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(paymentGets()).toBe(1)
    expect(screen.getByText('Төлбөрийг шалгаж байна…')).toBeInTheDocument()
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(paymentGets()).toBe(2)
    await act(() => vi.advanceTimersByTimeAsync(3000))
    expect(paymentGets()).toBe(3)

    expect(await screen.findByText('Төлбөр амжилттай төлөгдлөө', { selector: 'div' })).toBeInTheDocument()
    expect(toast.success).toHaveBeenCalledWith('Төлбөр амжилттай төлөгдлөө')
    expect(onPaid).toHaveBeenCalledWith(expect.objectContaining({ id: 'pay1', status: 'paid' }))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: billingKeys.subscription })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: billingKeys.invoices })

    // Polling stops once the payment is terminal.
    await act(() => vi.advanceTimersByTimeAsync(9000))
    expect(paymentGets()).toBe(3)
  })

  it('renders the provider QR image when present and shows the expired state', async () => {
    fakeLive()
    const pending = makePayment({ qrImage: 'iVBORw0KGgo=', expiresAt: new Date(Date.now() + 4000).toISOString() })
    post.mockResolvedValue({ payment: pending })
    get.mockResolvedValue({ payment: pending })
    renderWith(<PayDialog invoice={makeInvoice()} onClose={vi.fn()} />)
    fireEvent.click(screen.getByRole('button', { name: 'QR код үүсгэх' }))
    const img = await screen.findByRole('img', { name: 'Төлбөрийн QR код' })
    expect(img.tagName.toLowerCase()).toBe('img')
    expect(img).toHaveAttribute('src', 'data:image/png;base64,iVBORw0KGgo=')

    await act(() => vi.advanceTimersByTimeAsync(5000))
    expect(await screen.findByText('QR кодын хугацаа дууссан')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Дахин үүсгэх/ })).toBeInTheDocument()
  })

  it('offers the Mock provider in DEV and refetches on billing.updated', async () => {
    const live = fakeLive()
    const pending = makePayment({ provider: 'mock' })
    post.mockResolvedValue({ payment: pending })
    let paid = false
    get.mockImplementation(async (path: string) => {
      if (path === '/billing/payments/pay1') return { payment: paid ? { ...pending, status: 'paid' } : pending }
      return {}
    })
    renderWith(<PayDialog invoice={makeInvoice()} onClose={vi.fn()} />)
    expect(import.meta.env.DEV).toBe(true)
    fireEvent.click(screen.getByRole('radio', { name: 'Mock (хөгжүүлэлт)' }))
    fireEvent.click(screen.getByRole('button', { name: 'QR код үүсгэх' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/billing/invoices/inv1/pay', { provider: 'mock' }))
    await screen.findByText('Төлбөрийг шалгаж байна…')

    paid = true
    act(() => live.emit('billing.updated', { subscription: null }))
    expect(await screen.findByText('Төлбөр амжилттай төлөгдлөө', { selector: 'div' })).toBeInTheDocument()
  })
})
