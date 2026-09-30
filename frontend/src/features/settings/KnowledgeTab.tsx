import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { toast } from 'sonner'
import { BookOpen, ChevronDown, ChevronRight, FileText, Layers, Lock, Pencil, Plus, Trash2 } from 'lucide-react'
import { Badge, Button, Card, CardBody, Dialog, EmptyState, Field, Input, Select, Skeleton, Textarea } from '@/components/ui'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { fmtDateTime } from '@/lib/utils'
import type { KnowledgeBase, LLMConfig } from '@/lib/types'
import {
  useCreateKnowledgeBase, useDeleteKnowledgeBase, useKnowledgeBases, useLLMConfigs, useUpdateKnowledgeBase, type KnowledgeBaseBody,
} from './hooks'
import { ErrorNote, errMsg } from './common'
import KnowledgeBaseDetail from './KnowledgeBaseDetail'

export const DEFAULT_CHUNK_SIZE = 1200
export const DEFAULT_CHUNK_OVERLAP = 200

const EMBEDDING_PLACEHOLDERS: Record<string, string> = {
  openai: 'text-embedding-3-small',
  google: 'text-embedding-004',
  ollama: 'nomic-embed-text',
  openai_compatible: 'Загварын нэр (заавал)',
}

/** Placeholder for the embedding model input, chosen by the provider of the selected (or default) LLM config. */
export function embeddingPlaceholder(provider?: string): string {
  return (provider && EMBEDDING_PLACEHOLDERS[provider]) || 'Embedding загварын нэр'
}

export function KnowledgeBaseDialog({ open, onClose, initial, llmConfigs }: {
  open: boolean; onClose: () => void; initial?: KnowledgeBase | null; llmConfigs: LLMConfig[]
}) {
  if (!open) return null
  return <KnowledgeBaseForm key={initial?.id ?? 'new'} initial={initial} llmConfigs={llmConfigs} onClose={onClose} />
}

function KnowledgeBaseForm({ initial, llmConfigs, onClose }: { initial?: KnowledgeBase | null; llmConfigs: LLMConfig[]; onClose: () => void }) {
  const create = useCreateKnowledgeBase()
  const update = useUpdateKnowledgeBase()
  const locked = !!initial && initial.chunkCount > 0
  const [name, setName] = useState(initial?.name ?? '')
  const [description, setDescription] = useState(initial?.description ?? '')
  const [configId, setConfigId] = useState(initial?.embeddingLlmConfigId ?? '')
  const [model, setModel] = useState(initial?.embeddingModel ?? '')
  const [chunkSize, setChunkSize] = useState(initial ? String(initial.chunkSize) : '')
  const [chunkOverlap, setChunkOverlap] = useState(initial ? String(initial.chunkOverlap) : '')
  const [advanced, setAdvanced] = useState(false)
  const [touched, setTouched] = useState(false)

  const selected = llmConfigs.find((c) => c.id === configId) ?? (configId ? undefined : llmConfigs.find((c) => c.isDefault))
  const size = chunkSize.trim() ? Number(chunkSize) : undefined
  const overlap = chunkOverlap.trim() ? Number(chunkOverlap) : undefined
  const nameError = touched && !name.trim() ? 'Нэр оруулна уу' : null
  let chunkError: string | null = null
  if (touched && !locked) {
    if (size !== undefined && (!Number.isFinite(size) || size < 100)) chunkError = 'Хэсгийн хэмжээ 100-аас багагүй байх ёстой'
    else if (overlap !== undefined && (!Number.isFinite(overlap) || overlap < 0)) chunkError = 'Давхцал 0-ээс багагүй байх ёстой'
    else if ((overlap ?? DEFAULT_CHUNK_OVERLAP) >= (size ?? DEFAULT_CHUNK_SIZE)) chunkError = 'Давхцал хэсгийн хэмжээнээс бага байх ёстой'
  }

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (!name.trim()) return
    if (!locked) {
      if (size !== undefined && (!Number.isFinite(size) || size < 100)) { setAdvanced(true); return }
      if (overlap !== undefined && (!Number.isFinite(overlap) || overlap < 0)) { setAdvanced(true); return }
      if ((overlap ?? DEFAULT_CHUNK_OVERLAP) >= (size ?? DEFAULT_CHUNK_SIZE)) { setAdvanced(true); return }
    }
    const body: KnowledgeBaseBody = { name: name.trim(), description: description.trim() }
    if (!locked) {
      body.embeddingLlmConfigId = configId || null
      body.embeddingModel = model.trim() || undefined
      body.chunkSize = size
      body.chunkOverlap = overlap
    }
    const opts = {
      onSuccess: () => { toast.success(initial ? 'Мэдлэгийн сан шинэчлэгдлээ' : 'Мэдлэгийн сан үүслээ'); onClose() },
      onError: (err: unknown) => toast.error(errMsg(err)),
    }
    if (initial) update.mutate({ id: initial.id, body }, opts)
    else create.mutate(body, opts)
  }

  const pending = create.isPending || update.isPending
  return (
    <Dialog open onClose={onClose} title={initial ? 'Мэдлэгийн сан засах' : 'Шинэ мэдлэгийн сан'} className="max-w-xl"
      description="Баримт байршуулж, дуут агентад хариулт өгөхөд ашиглуулна.">
      <form onSubmit={submit} className="space-y-4" noValidate>
        <Field label="Нэр" error={nameError}>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Бүтээгдэхүүний заавар" autoFocus />
        </Field>
        <Field label="Тайлбар" hint="Заавал биш">
          <Textarea rows={2} className="min-h-16" value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field>

        {locked && (
          <div className="flex items-start gap-2 rounded-md border border-[var(--warning-border)] bg-[var(--warning-soft)] px-3 py-2 text-xs text-[var(--warning-fg)]" data-testid="kb-lock-hint">
            <Lock className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />
            <span>Баримт боловсруулагдсан ({initial?.chunkCount} хэсэг) тул embedding болон хэсэглэлтийн тохиргоог өөрчлөх боломжгүй. Өөрчлөх бол сангаа устгаад дахин үүсгэнэ үү.</span>
          </div>
        )}

        <Field label="Embedding LLM тохиргоо" hint="Сонгосон LLM тохиргооны API түлхүүрийг embedding-д ашиглана. «Default» бол байгууллагын үндсэн LLM.">
          <Select value={configId} onChange={(e) => setConfigId(e.target.value)} disabled={locked} placeholder="Default"
            options={llmConfigs.map((c) => ({ value: c.id, label: `${c.name} · ${c.provider}${c.isDefault ? ' ★' : ''}` }))} />
        </Field>
        <Field label="Embedding загвар" hint="Хоосон бол провайдерын үндсэн загварыг ашиглана">
          <Input value={model} onChange={(e) => setModel(e.target.value)} disabled={locked} placeholder={embeddingPlaceholder(selected?.provider)} />
        </Field>

        <div>
          <button type="button" onClick={() => setAdvanced((v) => !v)} aria-expanded={advanced}
            className="flex items-center gap-1 text-xs font-medium text-[var(--fg-muted)] hover:text-[var(--fg)]">
            {advanced ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}Нарийвчилсан тохиргоо
          </button>
          {advanced && (
            <div className="mt-3 grid gap-4 sm:grid-cols-2">
              <Field label="Хэсгийн хэмжээ (тэмдэгт)" hint={`Анхдагч: ${DEFAULT_CHUNK_SIZE}`}>
                <Input type="number" min={100} value={chunkSize} onChange={(e) => setChunkSize(e.target.value)} disabled={locked} placeholder={String(DEFAULT_CHUNK_SIZE)} />
              </Field>
              <Field label="Давхцал (тэмдэгт)" hint={`Анхдагч: ${DEFAULT_CHUNK_OVERLAP}`}>
                <Input type="number" min={0} value={chunkOverlap} onChange={(e) => setChunkOverlap(e.target.value)} disabled={locked} placeholder={String(DEFAULT_CHUNK_OVERLAP)} />
              </Field>
              {chunkError && <div role="alert" className="text-xs text-[var(--danger)] sm:col-span-2">{chunkError}</div>}
            </div>
          )}
        </div>

        <ErrorNote error={create.error ?? update.error} />
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
          <Button type="submit" loading={pending}>Хадгалах</Button>
        </div>
      </form>
    </Dialog>
  )
}

export default function KnowledgeTab() {
  const { kbId } = useParams<{ kbId?: string }>()
  if (kbId) return <KnowledgeBaseDetail kbId={kbId} />
  return <KnowledgeList />
}

function KnowledgeList() {
  const navigate = useNavigate()
  const bases = useKnowledgeBases()
  const llm = useLLMConfigs()
  const del = useDeleteKnowledgeBase()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<KnowledgeBase | null>(null)
  const [deleting, setDeleting] = useState<KnowledgeBase | null>(null)
  const items = bases.data ?? []
  const openDetail = (id: string) => navigate(`/settings/knowledge/${id}`)

  return (
    <div className="space-y-4">
      <Card>
        <CardBody className="space-y-2 text-sm text-[var(--fg-muted)]">
          <div className="flex items-center gap-2 font-medium text-[var(--fg)]"><BookOpen className="h-4 w-4" />Мэдлэгийн сан гэж юу вэ?</div>
          <p>Байгууллагын өөрийн баримт (заавар, үнийн жагсаалт, түгээмэл асуулт г.м.)-аас AI агент хариулт олж ярина. Баримтыг байршуулахад автоматаар хэсэгчилж, embedding үүсгэж хадгална.</p>
          <p><b className="text-[var(--fg)]">Хэрэгтэй үед хайх (tool) горим:</b> агент асуулт бүрт хамгийн ойр хэсгүүдийг хайж, тэдгээрт тулгуурлан хариулна. Том сан, олон баримтад тохиромжтой.</p>
          <p><b className="text-[var(--fg)]">Бүхэлд нь оруулах (context) горим:</b> сангийн бүх текстийг системийн prompt-д шууд оруулна. Жижиг баримтад (60 000 тэмдэгт хүртэл) тохиромжтой.</p>
          <p>Embedding нь сонгосон LLM тохиргооны API түлхүүрийг ашиглана. Горимыг «Агент профайл» табаас сонгоно.</p>
        </CardBody>
      </Card>

      <div className="flex items-center justify-between gap-4">
        <h2 className="text-sm font-semibold text-[var(--fg)]">Мэдлэгийн сангууд</h2>
        <Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Сан үүсгэх</Button>
      </div>

      {bases.isLoading ? (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3"><Skeleton className="h-40" /><Skeleton className="h-40" /><Skeleton className="h-40" /></div>
      ) : bases.isError ? (
        <ErrorNote error={bases.error} />
      ) : items.length === 0 ? (
        <Card><EmptyState icon={<BookOpen className="h-8 w-8" />} title="Мэдлэгийн сан үүсээгүй байна" description="Эхний сангаа үүсгэж, баримтаа байршуулна уу."
          action={<Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Сан үүсгэх</Button>} /></Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {items.map((kb) => (
            <Card key={kb.id} className="flex cursor-pointer flex-col transition-colors hover:border-[var(--border-strong)]" onClick={() => openDetail(kb.id)} data-testid={`kb-card-${kb.id}`}>
              <CardBody className="flex-1 space-y-3">
                <div className="flex items-center gap-2">
                  <div className="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-[var(--accent)]/15 text-[var(--accent-fg)]"><BookOpen className="h-4 w-4" /></div>
                  <h3 className="min-w-0 truncate text-sm font-semibold text-[var(--fg)]">
                    <Link to={`/settings/knowledge/${kb.id}`} onClick={(e) => e.stopPropagation()} className="hover:underline">{kb.name}</Link>
                  </h3>
                </div>
                <p className="line-clamp-2 min-h-8 text-xs leading-relaxed text-[var(--fg-muted)]">{kb.description || 'Тайлбар оруулаагүй'}</p>
                <div className="flex flex-wrap gap-1.5">
                  <Badge tone="neutral"><FileText className="h-3 w-3" />{kb.documentCount} баримт</Badge>
                  <Badge tone="neutral"><Layers className="h-3 w-3" />{kb.chunkCount} хэсэг</Badge>
                  <Badge tone="info" className="font-mono">{kb.embeddingModel || 'default'}</Badge>
                </div>
                <div className="text-xs text-[var(--fg-subtle)]">Шинэчлэгдсэн: {fmtDateTime(kb.updatedAt)}</div>
              </CardBody>
              <div className="flex justify-end gap-1 border-t border-[var(--border)] px-3 py-2">
                <Button size="sm" variant="ghost" aria-label={`Засах ${kb.name}`} onClick={(e) => { e.stopPropagation(); setEditing(kb) }}><Pencil className="h-3.5 w-3.5" />Засах</Button>
                <Button size="sm" variant="ghost" aria-label={`Устгах ${kb.name}`} onClick={(e) => { e.stopPropagation(); setDeleting(kb) }}><Trash2 className="h-3.5 w-3.5 text-[var(--danger)]" />Устгах</Button>
              </div>
            </Card>
          ))}
        </div>
      )}

      <KnowledgeBaseDialog open={creating || editing !== null} initial={editing} llmConfigs={llm.data ?? []} onClose={() => { setCreating(false); setEditing(null) }} />
      <ConfirmDialog
        open={deleting !== null} onClose={() => setDeleting(null)} title="Мэдлэгийн сан устгах уу?" confirmLabel="Устгах"
        description={deleting ? `«${deleting.name}» сан болон доторх бүх баримт устана. Энэ санг ашиглаж буй агент профайлуудаас салгагдана.` : undefined}
        onConfirm={async () => {
          if (!deleting) return
          try { await del.mutateAsync(deleting.id); toast.success('Устгагдлаа') } catch (e) { toast.error(errMsg(e)); throw e }
        }}
      />
    </div>
  )
}
