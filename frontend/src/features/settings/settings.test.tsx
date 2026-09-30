import type { ReactNode } from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { AgentProfile, LLMCatalogEntry, LLMConfig, SIPNumber } from '@/lib/types'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

import { api } from '@/lib/api'
import { LLMConfigDialog } from './LLMConfigsTab'
import { SIPNumberDialog } from './SIPNumbersTab'
import { AgentProfileEditor } from './AgentProfilesTab'
import SettingsPage from './SettingsPage'

const mocked = vi.mocked(api)

const catalog: LLMCatalogEntry[] = [
  { provider: 'openai', label: 'OpenAI', models: ['gpt-4o', 'gpt-4o-mini'], needsApiKey: true, needsBaseUrl: false },
  { provider: 'anthropic', label: 'Anthropic', models: ['claude-sonnet-4-5'], needsApiKey: true, needsBaseUrl: false },
  { provider: 'openai_compatible', label: 'OpenAI-compatible', models: [], needsApiKey: true, needsBaseUrl: true },
  { provider: 'ollama', label: 'Ollama', models: ['llama3.1'], needsApiKey: false, needsBaseUrl: true },
]

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const baseConfig: LLMConfig = {
  id: 'c1', orgId: 'o', name: 'Main', provider: 'openai', model: 'gpt-4o', apiKeyHint: '…abcd', temperature: 0.5, maxTokens: 512,
  isDefault: true, fallbackId: null, createdAt: '', updatedAt: '',
}

beforeEach(() => { vi.clearAllMocks() })

describe('LLMConfigDialog', () => {
  it('shows baseUrl only for providers that need it and submits the right body', async () => {
    mocked.post.mockResolvedValue({ config: baseConfig })
    const onClose = vi.fn()
    wrap(<LLMConfigDialog open onClose={onClose} catalog={catalog} configs={[baseConfig]} />)

    expect(screen.queryByLabelText(/^Base URL/)).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/^Нэр/), { target: { value: 'Local' } })
    fireEvent.change(screen.getByLabelText(/^API түлхүүр/), { target: { value: 'sk-secret' } })
    fireEvent.change(screen.getByLabelText(/^Провайдер/), { target: { value: 'openai_compatible' } })
    const base = await screen.findByLabelText(/^Base URL/)
    fireEvent.change(base, { target: { value: 'https://llm.example.com/v1' } })
    fireEvent.change(screen.getByLabelText(/^Загвар/), { target: { value: 'my-model' } })
    fireEvent.change(screen.getByLabelText(/^Max tokens/), { target: { value: '2048' } })

    fireEvent.change(screen.getByLabelText(/^Провайдер/), { target: { value: 'anthropic' } })
    expect(screen.queryByLabelText(/^Base URL/)).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/^Провайдер/), { target: { value: 'openai_compatible' } })
    fireEvent.change(screen.getByLabelText(/^Base URL/), { target: { value: 'https://llm.example.com/v1' } })
    fireEvent.change(screen.getByLabelText(/^Загвар/), { target: { value: 'my-model' } })

    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    expect(mocked.post).toHaveBeenCalledWith('/llm-configs', {
      name: 'Local', provider: 'openai_compatible', model: 'my-model', baseUrl: 'https://llm.example.com/v1', apiKey: 'sk-secret',
      temperature: 0.7, maxTokens: 2048, isDefault: false, fallbackId: null,
    })
  })

  it('omits baseUrl for openai and blocks submit without an API key', async () => {
    mocked.post.mockResolvedValue({ config: baseConfig })
    wrap(<LLMConfigDialog open onClose={vi.fn()} catalog={catalog} configs={[]} />)
    fireEvent.change(screen.getByLabelText(/^Нэр/), { target: { value: 'GPT' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('API түлхүүр шаардлагатай')
    expect(mocked.post).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText(/^API түлхүүр/), { target: { value: 'sk-1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    const body = mocked.post.mock.calls[0][1] as Record<string, unknown>
    expect(body).not.toHaveProperty('baseUrl')
    expect(body).toMatchObject({ provider: 'openai', model: 'gpt-4o', apiKey: 'sk-1' })
  })

  it('on edit keeps the existing key (empty apiKey) and PUTs', async () => {
    mocked.put.mockResolvedValue({ config: baseConfig })
    wrap(<LLMConfigDialog open onClose={vi.fn()} initial={baseConfig} catalog={catalog} configs={[baseConfig]} />)
    expect(screen.getByLabelText(/^API түлхүүр/)).toHaveAttribute('placeholder', 'Одоогийн: …abcd')
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalled())
    expect(mocked.put).toHaveBeenCalledWith('/llm-configs/c1', expect.objectContaining({ apiKey: '', name: 'Main', temperature: 0.5, maxTokens: 512, isDefault: true }))
  })

  it('test button shows the reply and latency', async () => {
    mocked.post.mockResolvedValue({ ok: true, reply: 'Сайн уу', latencyMs: 321 })
    wrap(<LLMConfigDialog open onClose={vi.fn()} initial={baseConfig} catalog={catalog} configs={[]} />)
    fireEvent.click(screen.getByRole('button', { name: /Тест/ }))
    expect(await screen.findByText('Сайн уу')).toBeInTheDocument()
    expect(screen.getByText(/321 мс/)).toBeInTheDocument()
    expect(mocked.post).toHaveBeenCalledWith('/llm-configs/c1/test', expect.objectContaining({ prompt: expect.any(String) }))
  })
})

describe('SIPNumberDialog', () => {
  it('validates E.164 before submitting', async () => {
    mocked.post.mockResolvedValue({ sipNumber: {} })
    wrap(<SIPNumberDialog open onClose={vi.fn()} profiles={[{ id: 'p1', name: 'Sales' } as AgentProfile]} />)
    const input = screen.getByLabelText(/^Дугаар/)
    fireEvent.change(input, { target: { value: '12345' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('E.164')
    expect(mocked.post).not.toHaveBeenCalled()

    // Bare 8-digit Mongolian numbers are normalised to +976.
    fireEvent.change(input, { target: { value: '70001234' } })
    fireEvent.change(screen.getByLabelText(/^Агент профайл/), { target: { value: 'p1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    expect(mocked.post).toHaveBeenCalledWith('/sip-numbers', {
      number: '+97670001234', label: '', agentProfileId: 'p1', allowInbound: true, allowOutbound: true, asteriskEndpoint: undefined,
    })
  })

  it('sends switches state on edit including active', async () => {
    mocked.put.mockResolvedValue({ sipNumber: {} })
    const n: SIPNumber = { id: 's1', orgId: 'o', number: '+97670001234', label: 'Main', allowInbound: true, allowOutbound: true, active: true, routing: { businessHours: { timezone: '', weekdays: [], startTime: '', endTime: '', pacePerMinute: 0 }, afterHoursMessage: '', menuPrompt: '', menu: [], menuTimeoutSec: 8, menuRepeat: 1 }, createdAt: '', updatedAt: '' }
    wrap(<SIPNumberDialog open onClose={vi.fn()} initial={n} profiles={[]} />)
    fireEvent.click(screen.getByRole('switch', { name: 'Явах дуудлага' }))
    fireEvent.click(screen.getByRole('switch', { name: 'Идэвхтэй' }))
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalled())
    expect(mocked.put).toHaveBeenCalledWith('/sip-numbers/s1', expect.objectContaining({ allowInbound: true, allowOutbound: false, active: false }))
  })
})

describe('AgentProfileEditor', () => {
  it('toggles tools and saves the selection', async () => {
    mocked.post.mockResolvedValue({ profile: {} })
    wrap(<AgentProfileEditor open onClose={vi.fn()} llmConfigs={[baseConfig]} />)
    const endCall = screen.getByRole('checkbox', { name: /end_call/ })
    const lookup = screen.getByRole('checkbox', { name: /lookup_contact/ })
    expect(endCall).toBeChecked()
    expect(lookup).not.toBeChecked()
    fireEvent.click(lookup)
    fireEvent.click(endCall)
    expect(lookup).toBeChecked()
    expect(endCall).not.toBeChecked()

    fireEvent.change(screen.getByLabelText(/^Нэр/), { target: { value: 'Sales' } })
    fireEvent.change(screen.getByLabelText(/^LLM тохиргоо/), { target: { value: 'c1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    expect(mocked.post).toHaveBeenCalledWith('/agent-profiles', expect.objectContaining({ name: 'Sales', tools: ['lookup_contact'], llmConfigId: 'c1', language: 'mn' }))
  })

  it('requires a transfer number when transfer_call is enabled and inserts the template', async () => {
    wrap(<AgentProfileEditor open onClose={vi.fn()} llmConfigs={[]} />)
    fireEvent.change(screen.getByLabelText(/^Нэр/), { target: { value: 'X' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /transfer_call/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Шилжүүлэх дугаар оруулна уу')
    expect(mocked.post).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: /Монгол загвар/ }))
    expect((screen.getByLabelText(/^Системийн prompt/) as HTMLTextAreaElement).value).toContain('монгол хэлээр')
    expect(Number(screen.getByTestId('prompt-count').textContent?.split(' ')[0])).toBeGreaterThan(100)
  })
})

describe('AgentProfileEditor knowledge base', () => {
  const kbs = [
    { id: 'kb1', name: 'Гарын авлага', documentCount: 3 },
    { id: 'kb2', name: 'FAQ', documentCount: 1 },
  ]
  beforeEach(() => {
    mocked.get.mockImplementation(async (path: string) => (path === '/knowledge-bases' ? { items: kbs } : { items: [] }))
    mocked.post.mockResolvedValue({ profile: {} })
    mocked.put.mockResolvedValue({ profile: {} })
  })

  it('defaults to no base with the mode radios disabled and posts null/off', async () => {
    wrap(<AgentProfileEditor open onClose={vi.fn()} llmConfigs={[]} />)
    const select = screen.getByLabelText(/^Мэдлэгийн сан/) as HTMLSelectElement
    expect(select.value).toBe('')
    expect(within(select).getByRole('option', { name: 'Байхгүй' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: /Хэрэгтэй үед хайна/ })).toBeDisabled()
    fireEvent.change(screen.getByLabelText(/^Нэр/), { target: { value: 'A' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    expect(mocked.post).toHaveBeenCalledWith('/agent-profiles', expect.objectContaining({ knowledgeBaseId: null, knowledgeMode: 'off' }))
  })

  it('sends knowledgeBaseId and the chosen mode', async () => {
    wrap(<AgentProfileEditor open onClose={vi.fn()} llmConfigs={[]} />)
    await screen.findByRole('option', { name: /Гарын авлага/ })
    fireEvent.change(screen.getByLabelText(/^Мэдлэгийн сан/), { target: { value: 'kb1' } })
    // Picking a base switches the mode on (tool) by default.
    expect(screen.getByRole('radio', { name: /Хэрэгтэй үед хайна \(lookup_knowledge\)/ })).toBeChecked()
    fireEvent.click(screen.getByRole('radio', { name: /Бүхэлд нь prompt-д оруулна/ }))
    expect(screen.getByRole('radio', { name: /Бүхэлд нь prompt-д оруулна/ })).toBeChecked()
    fireEvent.change(screen.getByLabelText(/^Нэр/), { target: { value: 'A' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    expect(mocked.post).toHaveBeenCalledWith('/agent-profiles', expect.objectContaining({ knowledgeBaseId: 'kb1', knowledgeMode: 'context' }))
  })

  it('forces mode off when the base is cleared, and PUTs on edit', async () => {
    const profile = {
      id: 'p1', orgId: 'o', name: 'Sales', systemPrompt: '', greeting: '', language: 'mn', llmConfigId: null, sttProvider: 'faster_whisper', sttModel: 'large-v3',
      ttsProvider: 'piper', ttsVoice: '', maxDurationSec: 600, tools: ['end_call'], knowledgeBaseId: 'kb2', knowledgeMode: 'tool', createdAt: '', updatedAt: '',
    } as AgentProfile
    wrap(<AgentProfileEditor open onClose={vi.fn()} initial={profile} llmConfigs={[]} />)
    await screen.findByRole('option', { name: /FAQ/ })
    expect((screen.getByLabelText(/^Мэдлэгийн сан/) as HTMLSelectElement).value).toBe('kb2')
    expect(screen.getByRole('radio', { name: /Хэрэгтэй үед хайна/ })).toBeChecked()

    fireEvent.change(screen.getByLabelText(/^Мэдлэгийн сан/), { target: { value: '' } })
    expect(screen.getByRole('radio', { name: /Унтраасан/ })).toBeChecked()
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalled())
    expect(mocked.put).toHaveBeenCalledWith('/agent-profiles/p1', expect.objectContaining({ knowledgeBaseId: null, knowledgeMode: 'off' }))
  })
})

describe('SettingsPage', () => {
  it('renders the tab from the URL and lists SIP numbers with provisioning state', async () => {
    mocked.get.mockImplementation(async (path: string) => {
      if (path === '/sip-numbers') {
        return { items: [
          { id: 's1', orgId: 'o', number: '+97670001234', label: 'A', inboundTrunkId: 'ST_aaaaaaaaaa', outboundTrunkId: 'ST_bbbbbbbbbb', dispatchRuleId: 'SDR_cccccccc', allowInbound: true, allowOutbound: false, active: true, createdAt: '', updatedAt: '' },
          { id: 's2', orgId: 'o', number: '+97670009999', label: 'B', allowInbound: true, allowOutbound: true, active: true, createdAt: '', updatedAt: '' },
        ] }
      }
      return { items: [] }
    })
    wrap(
      <MemoryRouter initialEntries={['/settings/sip-numbers']}>
        <Routes><Route path="/settings/:tab" element={<SettingsPage />} /></Routes>
      </MemoryRouter>,
    )
    expect(await screen.findByText('+976 7000 1234')).toBeInTheDocument()
    const row = screen.getByText('+976 7000 9999').closest('tr') as HTMLElement
    expect(within(row).getByText('Провижн хийгдээгүй')).toBeInTheDocument()
    expect(within(row).getByRole('button', { name: /Провижн/ })).toBeInTheDocument()
    const ok = screen.getByText('+976 7000 1234').closest('tr') as HTMLElement
    expect(within(ok).getByLabelText(/^Провижн хийгдсэн/)).toBeInTheDocument()

    mocked.post.mockResolvedValue({ sipNumber: {} })
    fireEvent.click(within(row).getByRole('button', { name: /Провижн/ }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/sip-numbers/s2/provision'))
  })
})
