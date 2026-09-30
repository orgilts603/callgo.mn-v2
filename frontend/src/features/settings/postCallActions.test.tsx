import { useState } from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { PostCallAction, Webhook } from '@/lib/types'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }))

import { api } from '@/lib/api'
import { AgentProfileEditor } from './AgentProfilesTab'
import { PostCallActionsEditor, normalizePostCallActions, validatePostCallActions } from './PostCallActionsEditor'

const mocked = vi.mocked(api)
const hooks = [{ id: 'w1', url: 'https://crm.example.com/h', description: 'CRM' }] as Webhook[]

function Harness({ showErrors = false, onChange }: { showErrors?: boolean; onChange?: (v: PostCallAction[]) => void }) {
  const [v, setV] = useState<PostCallAction[]>([])
  return <PostCallActionsEditor value={v} showErrors={showErrors} webhooks={hooks} onChange={(n) => { setV(n); onChange?.(n) }} />
}

beforeEach(() => {
  vi.clearAllMocks()
  mocked.get.mockImplementation(async (path: string) => {
    if (path === '/webhooks') return { items: hooks }
    return { items: [] }
  })
})

describe('post-call action helpers', () => {
  it('validates per type', () => {
    const base = { outcomes: [], template: '', webhookId: null, delayMin: 0 }
    const errs = validatePostCallActions([
      { ...base, type: 'sms' }, { ...base, type: 'webhook' }, { ...base, type: 'callback', delayMin: 10081 }, { ...base, type: 'callback', delayMin: 30 },
    ])
    expect(errs[0].template).toBeTruthy()
    expect(errs[1].webhookId).toBeTruthy()
    expect(errs[2].delayMin).toBeTruthy()
    expect(errs[3]).toEqual({})
  })
  it('normalizes away fields of other types', () => {
    expect(normalizePostCallActions([{ type: 'webhook', outcomes: [' agreed ', ''], template: 'x', webhookId: 'w1', delayMin: 5 }]))
      .toEqual([{ type: 'webhook', outcomes: ['agreed'], template: '', webhookId: 'w1', delayMin: 0 }])
  })
})

describe('PostCallActionsEditor', () => {
  it('adds chips, placeholders and shows validation errors', () => {
    const onChange = vi.fn()
    render(<QueryClientProvider client={new QueryClient()}><Harness showErrors onChange={onChange} /></QueryClientProvider>)
    fireEvent.click(screen.getByRole('button', { name: /Үйлдэл нэмэх/ }))
    expect(screen.getByText('SMS загвар оруулна уу')).toBeInTheDocument()

    const chips = screen.getByLabelText('Үр дүнгийн код 1')
    fireEvent.change(chips, { target: { value: 'agreed' } })
    fireEvent.keyDown(chips, { key: 'Enter' })
    fireEvent.change(chips, { target: { value: 'custom_code' } })
    fireEvent.keyDown(chips, { key: ',' })
    expect(screen.getAllByTestId('outcome-chip')).toHaveLength(2)

    fireEvent.click(screen.getByRole('button', { name: '{{name}}' }))
    fireEvent.click(screen.getByRole('button', { name: '{{outcome}}' }))
    expect(screen.getByLabelText('SMS загвар 1')).toHaveValue('{{name}}{{outcome}}')
    expect(screen.queryByText('SMS загвар оруулна уу')).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Үйлдлийн төрөл 1'), { target: { value: 'webhook' } })
    expect(screen.getByText('Webhook сонгоно уу')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Webhook 1'), { target: { value: 'w1' } })
    expect(onChange).toHaveBeenLastCalledWith([{ type: 'webhook', outcomes: ['agreed', 'custom_code'], template: '{{name}}{{outcome}}', webhookId: 'w1', delayMin: 0 }])
  })
})

describe('AgentProfileEditor with post-call actions', () => {
  function renderEditor() {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
    return render(<QueryClientProvider client={qc}><AgentProfileEditor open onClose={vi.fn()} llmConfigs={[]} /></QueryClientProvider>)
  }

  it('blocks submit on an invalid action and sends postCallActions otherwise', async () => {
    mocked.post.mockResolvedValue({ profile: {} })
    renderEditor()
    fireEvent.change(screen.getByLabelText(/^Нэр/), { target: { value: 'Sales' } })
    fireEvent.click(screen.getByRole('button', { name: /Үйлдэл нэмэх/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    expect(await screen.findByText('SMS загвар оруулна уу')).toBeInTheDocument()
    expect(mocked.post).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText('Үйлдлийн төрөл 1'), { target: { value: 'callback' } })
    fireEvent.change(screen.getByLabelText('Хоцрох хугацаа 1'), { target: { value: '45' } })
    const chips = screen.getByLabelText('Үр дүнгийн код 1')
    fireEvent.change(chips, { target: { value: 'callback' } })
    fireEvent.keyDown(chips, { key: 'Enter' })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    expect(mocked.post).toHaveBeenCalledWith('/agent-profiles', expect.objectContaining({
      name: 'Sales', postCallActions: [{ type: 'callback', outcomes: ['callback'], template: '', webhookId: null, delayMin: 45 }],
    }))
  })
})
