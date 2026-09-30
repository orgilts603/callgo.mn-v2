import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Webhook, WebhookDelivery } from '@/lib/types'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }))

import { api } from '@/lib/api'
import IntegrationsTab from './IntegrationsTab'

const mocked = vi.mocked(api)

const hook: Webhook = {
  id: 'w1', orgId: 'o', url: 'https://crm.example.com/hook', secretHint: '…abcd', events: ['call.ended'], active: true, description: 'CRM',
  failureCount: 2, lastStatus: 500, lastAt: '2026-09-01T10:00:00Z', createdAt: '', updatedAt: '',
}
const delivery: WebhookDelivery = {
  id: 'd1', webhookId: 'w1', eventId: 'e1', eventType: 'system', status: 'failed', attempts: 2, responseCode: 500, lastError: 'boom', createdAt: '2026-09-01T10:00:00Z', updatedAt: '',
}

function featureError(): Error {
  return Object.assign(new Error('feature'), { status: 403, code: 'feature_unavailable' })
}

function setup(routes: Record<string, unknown> = {}) {
  mocked.get.mockImplementation(async (path: string) => {
    if (path in routes) {
      const v = routes[path]
      if (v instanceof Error) throw v
      return v
    }
    if (path === '/webhooks') return { items: [hook] }
    if (path === '/sms/config') return { provider: 'mock', from: 'CallGo', configured: true }
    if (path === '/sms') return { items: [], total: 0 }
    if (path === '/webhooks/w1/deliveries') return { items: [delivery], total: 1 }
    throw new Error(`unexpected GET ${path}`)
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={qc}><MemoryRouter><IntegrationsTab /></MemoryRouter></QueryClientProvider>)
}

beforeEach(() => { vi.clearAllMocks() })

describe('IntegrationsTab webhooks', () => {
  it('creates a webhook and shows the secret exactly once', async () => {
    mocked.post.mockResolvedValue({ webhook: hook, secret: 'whsec_supersecret' })
    setup()
    await screen.findByText('https://crm.example.com/hook')
    fireEvent.click(screen.getAllByRole('button', { name: /Webhook нэмэх/ })[0])

    fireEvent.change(screen.getByLabelText(/^URL/), { target: { value: 'not a url' } })
    fireEvent.click(screen.getByRole('button', { name: 'Үүсгэх' }))
    expect(await screen.findByText('URL буруу байна')).toBeInTheDocument()
    expect(mocked.post).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText(/^URL/), { target: { value: 'https://new.example.com/h' } })
    fireEvent.change(screen.getByLabelText(/^Тайлбар/), { target: { value: 'New' } })
    fireEvent.click(screen.getByLabelText(/Бүх үйл явдал/))
    fireEvent.click(screen.getByRole('button', { name: 'Үүсгэх' }))

    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/webhooks', { url: 'https://new.example.com/h', events: ['*'], description: 'New' }))
    expect(await screen.findByTestId('secret-value')).toHaveTextContent('whsec_supersecret')

    fireEvent.click(screen.getByRole('button', { name: /Хадгалсан, хаах/ }))
    await waitFor(() => expect(screen.queryByTestId('secret-value')).not.toBeInTheDocument())
    expect(screen.queryByText('whsec_supersecret')).not.toBeInTheDocument()
  })

  it('tests a webhook and shows the delivery result', async () => {
    mocked.post.mockResolvedValue({ delivery })
    setup()
    await screen.findByText('https://crm.example.com/hook')
    fireEvent.click(screen.getByRole('button', { name: /^Тест https/ }))
    const result = await screen.findByTestId('delivery-result')
    expect(mocked.post).toHaveBeenCalledWith('/webhooks/w1/test')
    expect(within(result).getByText('500')).toBeInTheDocument()
    expect(within(result).getByText('boom')).toBeInTheDocument()
  })

  it('toggles active via PUT and retries a delivery from the drawer', async () => {
    mocked.put.mockResolvedValue({ webhook: { ...hook, active: false } })
    mocked.post.mockResolvedValue({ delivery: { ...delivery, status: 'pending' } })
    setup()
    await screen.findByText('https://crm.example.com/hook')
    fireEvent.click(screen.getByRole('switch', { name: /Идэвхтэй https/ }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith('/webhooks/w1', { active: false }))

    fireEvent.click(screen.getByRole('button', { name: /^Хүргэлтүүд/ }))
    fireEvent.click(await screen.findByRole('button', { name: /Дахин илгээх system/ }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/webhook-deliveries/d1/retry'))
  })

  it('shows an upgrade card linking to billing when the feature is gated', async () => {
    setup({ '/webhooks': featureError(), '/sms/config': featureError() })
    await waitFor(() => expect(screen.getAllByTestId('upgrade-card')).toHaveLength(2))
    const cards = screen.getAllByTestId('upgrade-card')
    expect(within(cards[0]).getByRole('link')).toHaveAttribute('href', '/settings/billing')
  })
})

describe('IntegrationsTab SMS', () => {
  it('saves config without an empty apiKey and sends a test SMS', async () => {
    mocked.put.mockResolvedValue({ provider: 'http', from: 'CallGo', configured: true })
    mocked.post.mockResolvedValue({ message: { id: 'm1', orgId: 'o', to: '+97699112233', body: 'hi', provider: 'mock', status: 'sent', createdAt: '' } })
    setup()
    await screen.findByText('Тохируулсан')
    fireEvent.change(screen.getByLabelText(/^Провайдер/), { target: { value: 'http' } })
    fireEvent.change(screen.getByLabelText(/^Provider URL/), { target: { value: 'https://sms.example.mn/send' } })
    fireEvent.change(screen.getByLabelText(/^Мессежийн загвар/), { target: { value: 'Сайн уу {{name}}' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalledWith('/sms/config', { provider: 'http', from: 'CallGo', bodyTemplate: 'Сайн уу {{name}}', url: 'https://sms.example.mn/send' }))

    fireEvent.click(screen.getByRole('button', { name: /Тест SMS/ }))
    fireEvent.change(screen.getByLabelText(/^Хүлээн авагч/), { target: { value: '99112233' } })
    fireEvent.change(screen.getByLabelText(/^Мессеж$/), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: /Илгээх/ }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/sms/send', { to: '+97699112233', body: 'hi' }))
    expect(await screen.findByTestId('sms-result')).toHaveTextContent('Илгээсэн')
  })
})
