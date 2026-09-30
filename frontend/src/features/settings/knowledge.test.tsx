import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { KnowledgeBase, KnowledgeDocument, LLMConfig } from '@/lib/types'

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}))

import { toast } from 'sonner'
import { api } from '@/lib/api'
import SettingsPage from './SettingsPage'
import { fmtBytes } from './KnowledgeBaseDetail'

const mocked = vi.mocked(api)

const kb = (over: Partial<KnowledgeBase> = {}): KnowledgeBase => ({
  id: 'kb1', orgId: 'o', name: 'Гарын авлага', description: 'Бүтээгдэхүүний заавар', embeddingLlmConfigId: null, embeddingModel: 'text-embedding-3-small',
  embeddingDims: 1536, chunkSize: 1200, chunkOverlap: 200, documentCount: 3, chunkCount: 42, createdAt: '2026-01-02T03:04:05Z', updatedAt: '2026-02-03T04:05:06Z', ...over,
})
const doc = (over: Partial<KnowledgeDocument> = {}): KnowledgeDocument => ({
  id: 'd1', knowledgeBaseId: 'kb1', orgId: 'o', filename: 'manual.pdf', mimeType: 'application/pdf', sizeBytes: 2_621_440, status: 'ready',
  chunkCount: 12, charCount: 12345, createdAt: '2026-01-02T03:04:05Z', updatedAt: '2026-01-02T03:04:05Z', ...over,
})
const cfgs: LLMConfig[] = [
  { id: 'c1', orgId: 'o', name: 'Main', provider: 'openai', model: 'gpt-4o', apiKeyHint: '', temperature: 0.5, maxTokens: 512, isDefault: true, fallbackId: null, createdAt: '', updatedAt: '' },
  { id: 'c2', orgId: 'o', name: 'Local', provider: 'ollama', model: 'llama3.1', apiKeyHint: '', temperature: 0.5, maxTokens: 512, isDefault: false, fallbackId: null, createdAt: '', updatedAt: '' },
]

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/settings/:tab" element={<SettingsPage />} />
          <Route path="/settings/:tab/:kbId" element={<SettingsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

let bases: KnowledgeBase[]
let docs: KnowledgeDocument[]
let docsCalls: number
let docsFn: (call: number) => KnowledgeDocument[]

beforeEach(() => {
  vi.clearAllMocks()
  bases = [kb(), kb({ id: 'kb2', name: 'Шинэ сан', description: '', documentCount: 0, chunkCount: 0, embeddingModel: '' })]
  docs = [doc()]
  docsCalls = 0
  docsFn = () => docs
  mocked.get.mockImplementation(async (path: string) => {
    if (path === '/knowledge-bases') return { items: bases }
    if (path === '/llm-configs') return { items: cfgs }
    if (path === '/knowledge-bases/kb1') return { knowledgeBase: bases[0], documents: docs }
    if (path === '/knowledge-bases/kb1/documents') { docsCalls++; return { items: docsFn(docsCalls) } }
    if (path === '/knowledge-documents/d1') {
      return { document: docs[0], chunks: [{ id: 'ch1', seq: 0, heading: 'Тавтай морил', content: 'Эхний хэсгийн агуулга' }, { id: 'ch2', seq: 1, content: 'Хоёр дахь хэсэг' }] }
    }
    return { items: [] }
  })
})

describe('KnowledgeTab', () => {
  it('is registered as the "Мэдлэгийн сан" tab and lists bases with stats and the explanation', async () => {
    renderAt('/settings/knowledge')
    expect(screen.getByRole('link', { name: /Мэдлэгийн сан/ })).toHaveAttribute('href', '/settings/knowledge')
    expect(await screen.findByText('Гарын авлага')).toBeInTheDocument()
    expect(screen.getByText('Шинэ сан')).toBeInTheDocument()
    expect(screen.getByText(/Мэдлэгийн сан гэж юу вэ/)).toBeInTheDocument()
    expect(screen.getByText(/Хэрэгтэй үед хайх \(tool\) горим/)).toBeInTheDocument()
    const card = screen.getByTestId('kb-card-kb1')
    expect(within(card).getByText('3 баримт')).toBeInTheDocument()
    expect(within(card).getByText('42 хэсэг')).toBeInTheDocument()
    expect(within(card).getByText('text-embedding-3-small')).toBeInTheDocument()
    expect(within(card).getByText(/2026-02-03/)).toBeInTheDocument()
  })

  it('creates a base with the right body, provider-specific placeholder and collapsed advanced fields', async () => {
    mocked.post.mockResolvedValue({ knowledgeBase: kb() })
    renderAt('/settings/knowledge')
    fireEvent.click(await screen.findByRole('button', { name: /Сан үүсгэх/ }))
    const dialog = await screen.findByRole('dialog', { name: 'Шинэ мэдлэгийн сан' })
    expect(within(dialog).queryByLabelText(/^Хэсгийн хэмжээ/)).not.toBeInTheDocument()

    // Empty name is rejected.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Хадгалах' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Нэр оруулна уу')
    expect(mocked.post).not.toHaveBeenCalled()

    fireEvent.change(within(dialog).getByLabelText(/^Нэр/), { target: { value: 'FAQ' } })
    fireEvent.change(within(dialog).getByLabelText(/^Тайлбар/), { target: { value: 'Түгээмэл асуулт' } })
    // Default config (openai) placeholder, then switching to ollama changes it.
    await waitFor(() => expect(within(dialog).getByLabelText(/^Embedding загвар/)).toHaveAttribute('placeholder', 'text-embedding-3-small'))
    fireEvent.change(within(dialog).getByLabelText(/^Embedding LLM тохиргоо/), { target: { value: 'c2' } })
    expect(within(dialog).getByLabelText(/^Embedding загвар/)).toHaveAttribute('placeholder', 'nomic-embed-text')
    fireEvent.change(within(dialog).getByLabelText(/^Embedding загвар/), { target: { value: 'bge-m3' } })
    fireEvent.click(within(dialog).getByRole('button', { name: /Нарийвчилсан тохиргоо/ }))
    fireEvent.change(within(dialog).getByLabelText(/^Хэсгийн хэмжээ/), { target: { value: '800' } })
    fireEvent.change(within(dialog).getByLabelText(/^Давхцал/), { target: { value: '100' } })

    fireEvent.click(within(dialog).getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    expect(mocked.post).toHaveBeenCalledWith('/knowledge-bases', {
      name: 'FAQ', description: 'Түгээмэл асуулт', embeddingLlmConfigId: 'c2', embeddingModel: 'bge-m3', chunkSize: 800, chunkOverlap: 100,
    })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Шинэ мэдлэгийн сан' })).toBeNull())
  })

  it('sends null for the default LLM config and omits the model when empty', async () => {
    mocked.post.mockResolvedValue({ knowledgeBase: kb() })
    renderAt('/settings/knowledge')
    fireEvent.click(await screen.findByRole('button', { name: /Сан үүсгэх/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText(/^Нэр/), { target: { value: 'Min' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalled())
    const body = mocked.post.mock.calls[0][1] as Record<string, unknown>
    expect(body.embeddingLlmConfigId).toBeNull()
    expect(body.embeddingModel).toBeUndefined()
    expect(body.chunkSize).toBeUndefined()
  })

  it('locks embedding fields when the base already has chunks and only PUTs name/description', async () => {
    mocked.put.mockResolvedValue({ knowledgeBase: kb() })
    renderAt('/settings/knowledge')
    fireEvent.click(await screen.findByRole('button', { name: 'Засах Гарын авлага' }))
    const dialog = await screen.findByRole('dialog', { name: 'Мэдлэгийн сан засах' })
    expect(within(dialog).getByTestId('kb-lock-hint')).toBeInTheDocument()
    expect(within(dialog).getByLabelText(/^Embedding загвар/)).toBeDisabled()
    expect(within(dialog).getByLabelText(/^Embedding LLM тохиргоо/)).toBeDisabled()
    fireEvent.change(within(dialog).getByLabelText(/^Нэр/), { target: { value: 'Шинэчилсэн' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(mocked.put).toHaveBeenCalled())
    expect(mocked.put).toHaveBeenCalledWith('/knowledge-bases/kb1', { name: 'Шинэчилсэн', description: 'Бүтээгдэхүүний заавар' })
  })

  it('does not lock an empty base', async () => {
    renderAt('/settings/knowledge')
    fireEvent.click(await screen.findByRole('button', { name: 'Засах Шинэ сан' }))
    const dialog = await screen.findByRole('dialog', { name: 'Мэдлэгийн сан засах' })
    expect(within(dialog).queryByTestId('kb-lock-hint')).toBeNull()
    expect(within(dialog).getByLabelText(/^Embedding загвар/)).toBeEnabled()
  })

  it('deletes with a confirm that mentions the profiles being unlinked', async () => {
    mocked.delete.mockResolvedValue(undefined)
    renderAt('/settings/knowledge')
    fireEvent.click(await screen.findByRole('button', { name: 'Устгах Гарын авлага' }))
    expect(mocked.delete).not.toHaveBeenCalled()
    const dialog = screen.getByRole('dialog', { name: /Мэдлэгийн сан устгах уу/ })
    expect(within(dialog).getByText(/профайлуудаас салгагдана/)).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Устгах' }))
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith('/knowledge-bases/kb1'))
  })

  it('opens the detail route when a card is clicked', async () => {
    renderAt('/settings/knowledge')
    fireEvent.click(await screen.findByTestId('kb-card-kb1'))
    expect(await screen.findByRole('heading', { name: 'Гарын авлага' })).toBeInTheDocument()
    expect(await screen.findByText('manual.pdf')).toBeInTheDocument()
  })
})

describe('KnowledgeBaseDetail', () => {
  it('shows header stats and the documents table with humanised sizes and status badges', async () => {
    docs = [
      doc(),
      doc({ id: 'd2', filename: 'notes.txt', mimeType: 'text/plain', sizeBytes: 900, status: 'failed', error: 'PDF уншиж чадсангүй', chunkCount: 0, charCount: 0 }),
    ]
    renderAt('/settings/knowledge/kb1')
    expect(await screen.findByRole('heading', { name: 'Гарын авлага' })).toBeInTheDocument()
    expect(await screen.findByText('manual.pdf')).toBeInTheDocument()
    const row = screen.getByText('manual.pdf').closest('tr') as HTMLElement
    expect(within(row).getByText('2.5 MB')).toBeInTheDocument()
    expect(within(row).getByText('application/pdf')).toBeInTheDocument()
    expect(within(row).getByText('Бэлэн')).toBeInTheDocument()
    expect(within(row).getByText('12')).toBeInTheDocument()
    expect(within(row).getByText('12,345')).toBeInTheDocument()
    const failed = screen.getByText('notes.txt').closest('tr') as HTMLElement
    expect(within(failed).getByText('Алдаа')).toHaveAttribute('title', 'PDF уншиж чадсангүй')
    expect(within(failed).getByText('900 B')).toBeInTheDocument()
    // Back link (in addition to the tab nav link).
    expect(screen.getAllByRole('link', { name: /Мэдлэгийн сан/ }).every((a) => a.getAttribute('href') === '/settings/knowledge')).toBe(true)
    expect(screen.getAllByRole('link', { name: /Мэдлэгийн сан/ })).toHaveLength(2)
    expect(screen.getByText('3 баримт')).toBeInTheDocument()
    expect(screen.getByText('42 хэсэг')).toBeInTheDocument()
  })

  it('formats byte sizes', () => {
    expect(fmtBytes(0)).toBe('0 B')
    expect(fmtBytes(2048)).toBe('2.0 KB')
    expect(fmtBytes(300 * 1024)).toBe('300 KB')
    expect(fmtBytes(20 * 1024 * 1024)).toBe('20.0 MB')
  })

  it('uploads a file as multipart under "file"', async () => {
    mocked.post.mockResolvedValue({ document: doc({ status: 'processing' }) })
    renderAt('/settings/knowledge/kb1')
    await screen.findByText('manual.pdf')
    const file = new File(['hello'], 'faq.md', { type: 'text/markdown' })
    fireEvent.change(screen.getByLabelText('Баримт файл'), { target: { files: [file] } })
    await waitFor(() => expect(mocked.post).toHaveBeenCalledTimes(1))
    const [path, body] = mocked.post.mock.calls[0]
    expect(path).toBe('/knowledge-bases/kb1/documents')
    expect(body).toBeInstanceOf(FormData)
    expect((body as FormData).get('file')).toBe(file)
  })

  it('rejects unsupported types and files over 20 MB without calling the API', async () => {
    renderAt('/settings/knowledge/kb1')
    await screen.findByText('manual.pdf')
    const bad = new File(['x'], 'photo.png', { type: 'image/png' })
    const big = new File(['x'], 'huge.pdf', { type: 'application/pdf' })
    Object.defineProperty(big, 'size', { value: 21 * 1024 * 1024 })
    fireEvent.change(screen.getByLabelText('Баримт файл'), { target: { files: [bad, big] } })
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(2))
    expect(mocked.post).not.toHaveBeenCalled()
  })

  it('adds pasted text as a JSON body', async () => {
    mocked.post.mockResolvedValue({ document: doc({ status: 'processing' }) })
    renderAt('/settings/knowledge/kb1')
    fireEvent.click(await screen.findByRole('button', { name: /Текст оруулах/ }))
    const dialog = await screen.findByRole('dialog', { name: 'Текст оруулах' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Нэмэх' }))
    expect(await within(dialog).findByText('Нэр оруулна уу')).toBeInTheDocument()
    expect(mocked.post).not.toHaveBeenCalled()
    fireEvent.change(within(dialog).getByLabelText(/^Файлын нэр/), { target: { value: 'hours' } })
    fireEvent.change(within(dialog).getByLabelText(/^Текст/), { target: { value: 'Даваа-Баасан 9-18' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Нэмэх' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/knowledge-bases/kb1/documents', { filename: 'hours.txt', text: 'Даваа-Баасан 9-18' }))
  })

  it('polls the documents list every 3 s while a document is processing, then stops', async () => {
    docsFn = (call) => [doc({ id: 'd9', filename: 'big.pdf', status: call >= 2 ? 'ready' : 'processing' })]
    renderAt('/settings/knowledge/kb1')
    expect(await screen.findByText('Боловсруулж байна')).toBeInTheDocument()
    expect(docsCalls).toBe(1)
    await waitFor(() => expect(screen.getByText('Бэлэн')).toBeInTheDocument(), { timeout: 6000 })
    expect(docsCalls).toBe(2)
    // No more polling once nothing is processing.
    await new Promise((r) => setTimeout(r, 3400))
    expect(docsCalls).toBe(2)
  }, 15000)

  it('previews chunks in a drawer', async () => {
    renderAt('/settings/knowledge/kb1')
    fireEvent.click(await screen.findByRole('button', { name: 'Хэсгүүд харах manual.pdf' }))
    expect(await screen.findByText('Эхний хэсгийн агуулга')).toBeInTheDocument()
    expect(screen.getByText('Тавтай морил')).toBeInTheDocument()
    expect(screen.getByText('Хоёр дахь хэсэг')).toBeInTheDocument()
    expect(mocked.get).toHaveBeenCalledWith('/knowledge-documents/d1', { offset: 0 })
  })

  it('paginates chunks by 50', async () => {
    docs = [doc({ chunkCount: 120 })]
    renderAt('/settings/knowledge/kb1')
    fireEvent.click(await screen.findByRole('button', { name: 'Хэсгүүд харах manual.pdf' }))
    await screen.findByText('Эхний хэсгийн агуулга')
    fireEvent.click(screen.getByRole('button', { name: 'Дараах' }))
    await waitFor(() => expect(mocked.get).toHaveBeenCalledWith('/knowledge-documents/d1', { offset: 50 }))
  })

  it('reprocesses and deletes a document (with confirm)', async () => {
    mocked.post.mockResolvedValue(undefined)
    mocked.delete.mockResolvedValue(undefined)
    renderAt('/settings/knowledge/kb1')
    fireEvent.click(await screen.findByRole('button', { name: 'Дахин боловсруулах manual.pdf' }))
    await waitFor(() => expect(mocked.post).toHaveBeenCalledWith('/knowledge-documents/d1/reprocess'))

    fireEvent.click(screen.getByRole('button', { name: 'Устгах manual.pdf' }))
    expect(mocked.delete).not.toHaveBeenCalled()
    const dialog = screen.getByRole('dialog', { name: /Баримт устгах уу/ })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Устгах' }))
    await waitFor(() => expect(mocked.delete).toHaveBeenCalledWith('/knowledge-documents/d1'))
  })

  it('search panel renders hits with score, filename › heading, latency and hybrid mode', async () => {
    mocked.post.mockResolvedValue({
      hits: [
        { chunkId: 'c1', documentId: 'd1', filename: 'manual.pdf', heading: 'Хүргэлт', content: 'Хүргэлт 2-3 хоногт очно', score: 0.87 },
        { chunkId: 'c2', documentId: 'd1', filename: 'manual.pdf', content: 'Төлбөрийн нөхцөл', score: 0.31 },
      ],
      latencyMs: 42, mode: 'hybrid',
    })
    renderAt('/settings/knowledge/kb1')
    await screen.findByText('manual.pdf')
    fireEvent.change(screen.getByLabelText(/^Асуулт/), { target: { value: 'хүргэлт' } })
    fireEvent.change(screen.getByLabelText(/^Илэрц/), { target: { value: '8' } })
    fireEvent.click(screen.getByRole('button', { name: /Хайх/ }))
    const res = await screen.findByTestId('kb-search-result')
    expect(mocked.post).toHaveBeenCalledWith('/knowledge-bases/kb1/search', { query: 'хүргэлт', k: 8 })
    expect(within(res).getByText('Хүргэлт 2-3 хоногт очно')).toBeInTheDocument()
    expect(within(res).getByText((_, el) => el?.textContent === 'manual.pdf › Хүргэлт' && el.tagName === 'DIV')).toBeInTheDocument()
    expect(within(res).getByText('0.87')).toBeInTheDocument()
    const meters = within(res).getAllByRole('meter', { name: 'Оноо' })
    expect(meters).toHaveLength(2)
    expect(meters[0]).toHaveAttribute('aria-valuenow', '0.87')
    expect(within(res).getByText('42 мс')).toBeInTheDocument()
    expect(within(res).getByText('hybrid')).toBeInTheDocument()
    expect(within(res).queryByText(/embedding тохируулаагүй/)).toBeNull()
  })

  it('search panel explains text-only mode', async () => {
    mocked.post.mockResolvedValue({ hits: [], latencyMs: 7, mode: 'text' })
    renderAt('/settings/knowledge/kb1')
    await screen.findByText('manual.pdf')
    fireEvent.change(screen.getByLabelText(/^Асуулт/), { target: { value: 'x' } })
    fireEvent.click(screen.getByRole('button', { name: /Хайх/ }))
    const res = await screen.findByTestId('kb-search-result')
    expect(within(res).getByText('text')).toBeInTheDocument()
    expect(within(res).getByText(/embedding тохируулаагүй/)).toBeInTheDocument()
    expect(within(res).getByText('Тохирох хэсэг олдсонгүй.')).toBeInTheDocument()
    expect(mocked.post).toHaveBeenCalledWith('/knowledge-bases/kb1/search', { query: 'x', k: 5 })
  })
})
