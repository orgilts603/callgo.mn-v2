import { useEffect, useRef, useState, type DragEvent, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ArrowLeft, ChevronLeft, ChevronRight, Eye, FileText, Info, RefreshCw, Search, Trash2, Type, Upload } from 'lucide-react'
import {
  Badge, Button, Card, CardBody, CardHeader, Dialog, Drawer, EmptyState, Field, Input, Select, Skeleton, Table, TBody, TD, TH, THead, TR, Textarea,
} from '@/components/ui'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { cn, fmtDateTime } from '@/lib/utils'
import type { KnowledgeDocument, KnowledgeSearchResponse } from '@/lib/types'
import {
  CHUNK_PAGE_SIZE, knowledgeKey, useAddText, useDeleteDocument, useDocumentChunks, useKnowledgeBase, useKnowledgeDocuments,
  useKnowledgeSearch, useReprocessDocument, useUploadDocument,
} from './hooks'
import { ErrorNote, errMsg } from './common'

export const MAX_UPLOAD_BYTES = 20 * 1024 * 1024
export const ALLOWED_EXTENSIONS = ['pdf', 'docx', 'txt', 'md', 'csv'] as const

export function fmtBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

/** Returns a Mongolian error message, or null when the file may be uploaded. */
export function validateUpload(file: File): string | null {
  const ext = file.name.split('.').pop()?.toLowerCase() ?? ''
  if (!(ALLOWED_EXTENSIONS as readonly string[]).includes(ext)) return `«${file.name}»: зөвхөн ${ALLOWED_EXTENSIONS.join(', ')} файл дэмжинэ`
  if (file.size > MAX_UPLOAD_BYTES) return `«${file.name}»: файлын хэмжээ 20 MB-аас хэтэрлээ`
  return null
}

const statusTone = { processing: 'warning', ready: 'success', failed: 'danger' } as const
const statusLabel = { processing: 'Боловсруулж байна', ready: 'Бэлэн', failed: 'Алдаа' } as const

export function DocumentStatusBadge({ doc }: { doc: KnowledgeDocument }) {
  return (
    <Badge tone={statusTone[doc.status]} dot pulse={doc.status === 'processing'} title={doc.status === 'failed' ? doc.error || 'Алдаа гарлаа' : undefined}>
      {statusLabel[doc.status]}
    </Badge>
  )
}

function UploadZone({ kbId }: { kbId: string }) {
  const upload = useUploadDocument(kbId)
  const input = useRef<HTMLInputElement>(null)
  const [drag, setDrag] = useState(false)
  const [busy, setBusy] = useState(false)

  async function handle(files: FileList | File[]) {
    const list = Array.from(files)
    if (list.length === 0) return
    setBusy(true)
    for (const file of list) {
      const problem = validateUpload(file)
      if (problem) { toast.error(problem); continue }
      try {
        await upload.mutateAsync(file)
        toast.success(`«${file.name}» байршууллаа, боловсруулж байна`)
      } catch (e) {
        toast.error(errMsg(e))
      }
    }
    setBusy(false)
  }
  const onDrop = (e: DragEvent) => { e.preventDefault(); setDrag(false); void handle(e.dataTransfer.files) }

  return (
    <div
      onDragOver={(e) => { e.preventDefault(); setDrag(true) }} onDragLeave={() => setDrag(false)} onDrop={onDrop}
      className={cn('flex flex-col items-center gap-2 rounded-[var(--radius-lg)] border-2 border-dashed px-4 py-6 text-center transition-colors',
        drag ? 'border-[var(--accent)] bg-[var(--accent-soft)]' : 'border-[var(--border)] bg-[var(--surface-1)]')}
      data-testid="kb-upload-zone">
      <Upload className="h-6 w-6 text-[var(--fg-muted)]" aria-hidden />
      <div className="text-sm text-[var(--fg)]">Файлаа энд чирж оруулна уу</div>
      <div className="text-xs text-[var(--fg-muted)]">PDF, DOCX, TXT, MD, CSV · 20 MB хүртэл</div>
      <div className="mt-1 flex gap-2">
        <Button variant="secondary" size="sm" loading={busy} onClick={() => input.current?.click()}><Upload className="h-3.5 w-3.5" />Файл сонгох</Button>
        <TextDialogButton kbId={kbId} />
      </div>
      <input ref={input} type="file" multiple className="hidden" aria-label="Баримт файл"
        accept=".pdf,.docx,.txt,.md,.csv,application/pdf,text/plain,text/markdown,text/csv"
        onChange={(e) => { const files = e.target.files; if (files) void handle(Array.from(files)); e.target.value = '' }} />
    </div>
  )
}

function TextDialogButton({ kbId }: { kbId: string }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button variant="secondary" size="sm" onClick={() => setOpen(true)}><Type className="h-3.5 w-3.5" />Текст оруулах</Button>
      {open && <TextDialog kbId={kbId} onClose={() => setOpen(false)} />}
    </>
  )
}

function TextDialog({ kbId, onClose }: { kbId: string; onClose: () => void }) {
  const add = useAddText(kbId)
  const [filename, setFilename] = useState('')
  const [text, setText] = useState('')
  const [touched, setTouched] = useState(false)
  const nameError = touched && !filename.trim() ? 'Нэр оруулна уу' : null
  const textError = touched && !text.trim() ? 'Текст оруулна уу' : null

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (!filename.trim() || !text.trim()) return
    let name = filename.trim()
    if (!/\.[a-z0-9]+$/i.test(name)) name += '.txt'
    add.mutate({ filename: name, text }, {
      onSuccess: () => { toast.success('Текст байршууллаа, боловсруулж байна'); onClose() },
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <Dialog open onClose={onClose} title="Текст оруулах" description="Хуулж авсан текстийг баримт болгон нэмнэ." className="max-w-xl">
      <form onSubmit={submit} className="space-y-4 text-left" noValidate>
        <Field label="Файлын нэр" error={nameError}><Input value={filename} onChange={(e) => setFilename(e.target.value)} placeholder="түгээмэл-асуулт.md" autoFocus /></Field>
        <Field label="Текст" error={textError}><Textarea rows={10} value={text} onChange={(e) => setText(e.target.value)} placeholder="Баримтын агуулга…" /></Field>
        <ErrorNote error={add.error} />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
          <Button type="submit" loading={add.isPending}>Нэмэх</Button>
        </div>
      </form>
    </Dialog>
  )
}

function ChunksDrawer({ doc, onClose }: { doc: KnowledgeDocument | null; onClose: () => void }) {
  const [offset, setOffset] = useState(0)
  const [lastId, setLastId] = useState<string | null>(null)
  if ((doc?.id ?? null) !== lastId) { setLastId(doc?.id ?? null); setOffset(0) }
  const chunks = useDocumentChunks(doc?.id ?? null, offset)
  const total = chunks.data?.document.chunkCount ?? doc?.chunkCount ?? 0
  const items = chunks.data?.chunks ?? []
  return (
    <Drawer open={doc !== null} onClose={onClose} title={doc ? `Хэсгүүд: ${doc.filename}` : ''}>
      {doc && (
        <div className="space-y-3 p-4">
          {chunks.isLoading ? (
            <><Skeleton className="h-20" /><Skeleton className="h-20" /></>
          ) : chunks.isError ? (
            <ErrorNote error={chunks.error} />
          ) : items.length === 0 ? (
            <EmptyState icon={<FileText className="h-8 w-8" />} title="Хэсэг олдсонгүй" description="Баримт боловсруулагдаагүй эсвэл хоосон байна." />
          ) : (
            <ol className="space-y-3" data-testid="chunk-list">
              {items.map((c) => (
                <li key={c.id} className="rounded-md border border-[var(--border)] bg-[var(--surface-0)] p-3">
                  <div className="mb-1 flex items-center gap-2 text-xs text-[var(--fg-subtle)]">
                    <Badge tone="neutral">#{c.seq + 1}</Badge>
                    {c.heading && <span className="truncate font-medium text-[var(--fg-muted)]">{c.heading}</span>}
                  </div>
                  <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-[var(--fg)]">{c.content}</p>
                </li>
              ))}
            </ol>
          )}
          {total > CHUNK_PAGE_SIZE && (
            <div className="flex items-center justify-between text-xs text-[var(--fg-muted)]">
              <span>{offset + 1}–{Math.min(total, offset + CHUNK_PAGE_SIZE)} / {total}</span>
              <div className="flex gap-1">
                <Button size="sm" variant="ghost" aria-label="Өмнөх" disabled={offset === 0} onClick={() => setOffset((o) => Math.max(0, o - CHUNK_PAGE_SIZE))}><ChevronLeft className="h-4 w-4" /></Button>
                <Button size="sm" variant="ghost" aria-label="Дараах" disabled={offset + CHUNK_PAGE_SIZE >= total} onClick={() => setOffset((o) => o + CHUNK_PAGE_SIZE)}><ChevronRight className="h-4 w-4" /></Button>
              </div>
            </div>
          )}
        </div>
      )}
    </Drawer>
  )
}

export function SearchPanel({ kbId }: { kbId: string }) {
  const search = useKnowledgeSearch(kbId)
  const [query, setQuery] = useState('')
  const [k, setK] = useState('5')
  const [result, setResult] = useState<KnowledgeSearchResponse | null>(null)

  function submit(e: FormEvent) {
    e.preventDefault()
    const q = query.trim()
    if (!q) return
    search.mutate({ query: q, k: Number(k) }, {
      onSuccess: setResult,
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <Card>
      <CardHeader title="Хайлт шалгах" description="Агент яг ийм байдлаар хайлт хийнэ. Асуултаа бичээд үр дүнг харна уу." />
      <CardBody className="space-y-4">
        <form onSubmit={submit} className="flex flex-wrap items-end gap-3">
          <Field label="Асуулт" className="min-w-64 flex-1">
            <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Жишээ: Хүргэлт хэдэн хоногт очих вэ?" />
          </Field>
          <Field label="Илэрц (k)" className="w-24">
            <Select value={k} onChange={(e) => setK(e.target.value)} options={['3', '5', '8', '10'].map((v) => ({ value: v, label: v }))} />
          </Field>
          <Button type="submit" loading={search.isPending} disabled={!query.trim()}><Search className="h-4 w-4" />Хайх</Button>
        </form>

        {result && (
          <div className="space-y-3" data-testid="kb-search-result">
            <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--fg-muted)]">
              <span>{result.hits.length} илэрц</span>
              <span className="tabular-nums">{result.latencyMs} мс</span>
              <Badge tone={result.mode === 'hybrid' ? 'accent' : 'warning'}>{result.mode}</Badge>
            </div>
            {result.mode === 'text' && (
              <div className="flex items-start gap-2 rounded-md border border-[var(--warning-border)] bg-[var(--warning-soft)] px-3 py-2 text-xs text-[var(--warning-fg)]">
                <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden />
                <span>Зөвхөн текстээр хайлаа: embedding тохируулаагүй эсвэл ашиглах боломжгүй байна. Утгаар хайхын тулд сангийн embedding LLM тохиргоог шалгана уу.</span>
              </div>
            )}
            {result.hits.length === 0 ? (
              <p className="text-sm text-[var(--fg-muted)]">Тохирох хэсэг олдсонгүй.</p>
            ) : (
              <ol className="space-y-3">
                {result.hits.map((h) => (
                  <li key={h.chunkId} className="rounded-md border border-[var(--border)] bg-[var(--surface-0)] p-3">
                    <div className="mb-2 flex items-center gap-3">
                      <div className="min-w-0 flex-1 truncate text-xs font-medium text-[var(--fg-muted)]">
                        {h.filename}{h.heading ? <> <span aria-hidden>›</span> {h.heading}</> : null}
                      </div>
                      <div className="flex shrink-0 items-center gap-2">
                        <div className="h-1.5 w-24 overflow-hidden rounded-full bg-[var(--surface-3)]" role="meter" aria-label="Оноо" aria-valuemin={0} aria-valuemax={1} aria-valuenow={h.score}>
                          <div className="h-full rounded-full bg-[var(--accent)]" style={{ width: `${Math.round(Math.min(1, Math.max(0, h.score)) * 100)}%` }} />
                        </div>
                        <span className="w-10 text-right text-xs tabular-nums text-[var(--fg-muted)]">{h.score.toFixed(2)}</span>
                      </div>
                    </div>
                    <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-[var(--fg)]">{h.content}</p>
                  </li>
                ))}
              </ol>
            )}
          </div>
        )}
      </CardBody>
    </Card>
  )
}

export default function KnowledgeBaseDetail({ kbId: kbIdProp }: { kbId?: string }) {
  const params = useParams<{ kbId?: string }>()
  const kbId = kbIdProp ?? params.kbId ?? ''
  const qc = useQueryClient()
  const kb = useKnowledgeBase(kbId)
  const docs = useKnowledgeDocuments(kbId)
  const del = useDeleteDocument()
  const reprocess = useReprocessDocument()
  const [preview, setPreview] = useState<KnowledgeDocument | null>(null)
  const [deleting, setDeleting] = useState<KnowledgeDocument | null>(null)

  const items = docs.data ?? []
  const processing = items.some((d) => d.status === 'processing')
  const wasProcessing = useRef(false)
  // When the last document finishes, refresh the base header counters.
  useEffect(() => {
    if (wasProcessing.current && !processing) void qc.invalidateQueries({ queryKey: [...knowledgeKey, 'base', kbId] })
    wasProcessing.current = processing
  }, [processing, qc, kbId])

  const base = kb.data?.knowledgeBase

  return (
    <div className="space-y-4">
      <div>
        <Link to="/settings/knowledge" className="inline-flex items-center gap-1 text-xs text-[var(--fg-muted)] hover:text-[var(--fg)]"><ArrowLeft className="h-3.5 w-3.5" />Мэдлэгийн сан</Link>
        {kb.isLoading ? (
          <Skeleton className="mt-2 h-14" />
        ) : kb.isError ? (
          <div className="mt-2"><ErrorNote error={kb.error} /></div>
        ) : base ? (
          <div className="mt-2 space-y-2">
            <h2 className="text-lg font-semibold tracking-[-0.015em] text-[var(--fg)]">{base.name}</h2>
            {base.description && <p className="max-w-3xl text-[13px] text-[var(--fg-muted)]">{base.description}</p>}
            <div className="flex flex-wrap gap-1.5">
              <Badge tone="neutral">{base.documentCount} баримт</Badge>
              <Badge tone="neutral">{base.chunkCount} хэсэг</Badge>
              <Badge tone="info" className="font-mono">{base.embeddingModel || 'default'}</Badge>
              {base.embeddingDims > 0 && <Badge tone="neutral">{base.embeddingDims} dims</Badge>}
            </div>
          </div>
        ) : null}
      </div>

      <UploadZone kbId={kbId} />

      <Card>
        <CardHeader title="Баримтууд" description={processing ? 'Боловсруулж байна… жагсаалт автоматаар шинэчлэгдэнэ' : `${items.length} баримт`} />
        {docs.isLoading ? (
          <CardBody className="space-y-2"><Skeleton className="h-9" /><Skeleton className="h-9" /></CardBody>
        ) : docs.isError ? (
          <CardBody><ErrorNote error={docs.error} /></CardBody>
        ) : items.length === 0 ? (
          <EmptyState icon={<FileText className="h-8 w-8" />} title="Баримт байршуулаагүй байна" description="Дээрх талбараар файл оруулах эсвэл текст хуулж нэмнэ үү." />
        ) : (
          <Table>
            <THead>
              <TR><TH>Файл</TH><TH>Төрөл</TH><TH>Хэмжээ</TH><TH>Төлөв</TH><TH className="text-right">Хэсэг</TH><TH className="text-right">Тэмдэгт</TH><TH>Огноо</TH><TH className="text-right">Үйлдэл</TH></TR>
            </THead>
            <TBody>
              {items.map((d) => (
                <TR key={d.id}>
                  <TD className="max-w-56 truncate font-medium" title={d.filename}>{d.filename}</TD>
                  <TD className="text-xs text-[var(--fg-muted)]">{d.mimeType || '—'}</TD>
                  <TD className="whitespace-nowrap text-xs tabular-nums text-[var(--fg-muted)]">{fmtBytes(d.sizeBytes)}</TD>
                  <TD><DocumentStatusBadge doc={d} /></TD>
                  <TD className="text-right tabular-nums">{d.chunkCount}</TD>
                  <TD className="text-right tabular-nums">{d.charCount.toLocaleString('en-US')}</TD>
                  <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtDateTime(d.createdAt)}</TD>
                  <TD className="text-right">
                    <div className="flex justify-end gap-0.5">
                      <Button size="icon" variant="ghost" aria-label={`Хэсгүүд харах ${d.filename}`} title="Хэсгүүд харах" disabled={d.status !== 'ready'} onClick={() => setPreview(d)}><Eye /></Button>
                      <Button size="icon" variant="ghost" aria-label={`Дахин боловсруулах ${d.filename}`} title="Дахин боловсруулах" disabled={d.status === 'processing' || reprocess.isPending}
                        onClick={() => reprocess.mutate(d.id, { onSuccess: () => toast.success('Дахин боловсруулж байна'), onError: (e) => toast.error(errMsg(e)) })}><RefreshCw /></Button>
                      <Button size="icon" variant="ghost" aria-label={`Устгах ${d.filename}`} title="Устгах" onClick={() => setDeleting(d)}><Trash2 className="text-[var(--danger)]" /></Button>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <SearchPanel kbId={kbId} />

      <ChunksDrawer doc={preview} onClose={() => setPreview(null)} />
      <ConfirmDialog
        open={deleting !== null} onClose={() => setDeleting(null)} title="Баримт устгах уу?" confirmLabel="Устгах"
        description={deleting ? `«${deleting.filename}» баримт болон түүний бүх хэсэг устана.` : undefined}
        onConfirm={async () => {
          if (!deleting) return
          try { await del.mutateAsync(deleting.id); toast.success('Устгагдлаа') } catch (e) { toast.error(errMsg(e)); throw e }
        }}
      />
    </div>
  )
}
