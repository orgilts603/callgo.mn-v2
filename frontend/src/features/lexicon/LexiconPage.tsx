import { useEffect, useState, type FormEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowRight, BookOpen, Pencil, Plus, Sparkles, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { useLive } from '@/lib/ws'
import { fmtDateTime } from '@/lib/utils'
import type { LexiconCorrection, LexiconScope, LexiconUpdatedPayload, ListResponse } from '@/lib/types'
import {
  Badge, Button, Card, CardBody, CardHeader, Dialog, EmptyState, Field, Input, PageHeader, Select, Skeleton, Table, TBody, TD, Textarea, TH, THead, TR,
} from '@/components/ui'
import { highlightHits, type LexiconHit } from './highlight'

const LEXICON_KEY = ['lexicon'] as const

const scopeLabel: Record<LexiconScope, string> = { stt: 'STT', tts: 'TTS', both: 'STT + TTS' }
const scopeTone: Record<LexiconScope, 'info' | 'accent' | 'success'> = { stt: 'info', tts: 'accent', both: 'success' }

interface LexiconBody { wrong: string; correct: string; scope: LexiconScope; phonetic?: string }

export function patchLexiconCache(old: ListResponse<LexiconCorrection> | undefined, p: LexiconUpdatedPayload): ListResponse<LexiconCorrection> | undefined {
  if (!old) return old
  const { correction, action } = p
  const exists = old.items.some((x) => x.id === correction.id)
  if (action === 'deleted') {
    return exists ? { items: old.items.filter((x) => x.id !== correction.id), total: Math.max(0, old.total - 1) } : old
  }
  if (exists) return { ...old, items: old.items.map((x) => (x.id === correction.id ? correction : x)) }
  return { items: [correction, ...old.items], total: old.total + 1 }
}

function EditDialog({ open, item, onClose }: { open: boolean; item: LexiconCorrection | null; onClose: () => void }) {
  const qc = useQueryClient()
  const [wrong, setWrong] = useState('')
  const [correct, setCorrect] = useState('')
  const [scope, setScope] = useState<LexiconScope>('both')
  const [phonetic, setPhonetic] = useState('')

  useEffect(() => {
    if (!open) return
    setWrong(item?.wrong ?? ''); setCorrect(item?.correct ?? ''); setScope(item?.scope ?? 'both'); setPhonetic(item?.phonetic ?? '')
  }, [open, item])

  const save = useMutation({
    mutationFn: (body: LexiconBody) => (item ? api.put<{ correction: LexiconCorrection }>(`/lexicon/${item.id}`, body) : api.post<{ correction: LexiconCorrection }>('/lexicon', body)),
    onSuccess: () => {
      toast.success('Хадгалагдлаа')
      void qc.invalidateQueries({ queryKey: LEXICON_KEY })
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const valid = wrong.trim() !== '' && correct.trim() !== ''
  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!valid) return
    save.mutate({ wrong: wrong.trim(), correct: correct.trim(), scope, phonetic: phonetic.trim() || undefined })
  }

  return (
    <Dialog open={open} onClose={onClose} title={item ? 'Засвар өөрчлөх' : 'Шинэ засвар'}
      footer={<>
        <Button variant="ghost" type="button" onClick={onClose}>Болих</Button>
        <Button type="submit" form="lexicon-form" disabled={!valid} loading={save.isPending}>Хадгалах</Button>
      </>}>
      <form id="lexicon-form" onSubmit={submit} className="space-y-4">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Буруу үг"><Input value={wrong} onChange={(e) => setWrong(e.target.value)} placeholder="кол гоу" autoFocus /></Field>
          <Field label="Зөв үг"><Input value={correct} onChange={(e) => setCorrect(e.target.value)} placeholder="CallGo" /></Field>
        </div>
        <Field label="Хамрах хүрээ">
          <Select value={scope} onChange={(e) => setScope(e.target.value as LexiconScope)}
            options={[{ value: 'both', label: 'STT + TTS' }, { value: 'stt', label: 'Зөвхөн STT (таних)' }, { value: 'tts', label: 'Зөвхөн TTS (унших)' }]} />
        </Field>
        <Field label="Дуудлага (phonetic)" hint="Piper TTS дуудлагын зөвлөмж">
          <Input value={phonetic} onChange={(e) => setPhonetic(e.target.value)} placeholder="кол гоу" />
        </Field>
      </form>
    </Dialog>
  )
}

function TryPanel() {
  const [text, setText] = useState('')
  const apply = useMutation({
    mutationFn: (t: string) => api.post<{ text: string; hits: LexiconHit[] }>('/lexicon/apply', { text: t }),
    onError: (e: Error) => toast.error(e.message),
  })
  const result = apply.data
  return (
    <Card>
      <CardHeader title="Туршилт" description="Текстийг lexicon-оор засаж үзэх (урьдчилан харах)" />
      <CardBody className="space-y-3">
        <Textarea aria-label="Туршилтын текст" value={text} onChange={(e) => setText(e.target.value)} placeholder="Засах текстээ энд бичнэ үү" />
        <Button size="sm" disabled={!text.trim()} loading={apply.isPending} onClick={() => apply.mutate(text)}><Sparkles className="h-4 w-4" />Туршиж үзэх</Button>
        {result && (
          <div className="space-y-2" data-testid="apply-result">
            <div className="rounded-md border border-[var(--border)] bg-[var(--surface-0)] p-3 text-sm leading-relaxed">{highlightHits(result.text, result.hits)}</div>
            {result.hits.length === 0 ? (
              <p className="text-xs text-[var(--fg-muted)]">Тохирох засвар олдсонгүй</p>
            ) : (
              <div className="flex flex-wrap gap-2">
                {result.hits.map((h) => <Badge key={h.id}>{h.wrong} <ArrowRight className="h-3 w-3" /> {h.correct}</Badge>)}
              </div>
            )}
          </div>
        )}
      </CardBody>
    </Card>
  )
}

export function LexiconPage() {
  const qc = useQueryClient()
  const [editing, setEditing] = useState<LexiconCorrection | null>(null)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [toDelete, setToDelete] = useState<LexiconCorrection | null>(null)

  const list = useQuery({ queryKey: LEXICON_KEY, queryFn: () => api.get<ListResponse<LexiconCorrection>>('/lexicon') })

  useEffect(() => {
    return useLive.getState().onEvent((ev) => {
      if (ev.type !== 'lexicon.updated') return
      const p = ev.payload as LexiconUpdatedPayload
      qc.setQueryData<ListResponse<LexiconCorrection>>(LEXICON_KEY, (old) => patchLexiconCache(old, p))
      toast(`Lexicon шинэчлэгдлээ: ${p.correction.wrong} → ${p.correction.correct}`)
    })
  }, [qc])

  const remove = useMutation({
    mutationFn: (id: string) => api.delete(`/lexicon/${id}`),
    onSuccess: () => {
      toast.success('Устгагдлаа')
      setToDelete(null)
      void qc.invalidateQueries({ queryKey: LEXICON_KEY })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const items = list.data?.items ?? []
  const openNew = () => { setEditing(null); setDialogOpen(true) }
  const openEdit = (c: LexiconCorrection) => { setEditing(c); setDialogOpen(true) }

  return (
    <div className="space-y-6">
      <PageHeader title="Lexicon" description="Тааруулсан үгсийн толь"
        actions={<Button size="sm" onClick={openNew}><Plus className="h-4 w-4" />Шинэ засвар</Button>} />

      <Card>
        <CardBody className="flex gap-3 text-sm text-[var(--fg-muted)]">
          <BookOpen className="mt-0.5 h-5 w-5 shrink-0 text-[var(--accent)]" />
          <div className="space-y-1">
            <div className="font-medium text-[var(--fg)]">Өөрөө сайжирдаг давталт</div>
            <p>
              Транскрипт дээрх буруу үг дээр дарж засахад энд автоматаар нэмэгдэнэ. Агент дараагийн дуудлагаас эхлэн
              STT-ээр танисан үгийг засаж, TTS-ээр зөв дуудна. Хэдэн удаа ажилласныг «Хэрэглэсэн» багана харуулна.
            </p>
          </div>
        </CardBody>
      </Card>

      <Card>
        {list.isLoading ? (
          <div className="space-y-2 p-4">{Array.from({ length: 5 }, (_, i) => <Skeleton key={i} className="h-9 w-full" />)}</div>
        ) : list.isError ? (
          <EmptyState title="Lexicon ачаалж чадсангүй" description={(list.error as Error).message} />
        ) : items.length === 0 ? (
          <EmptyState icon={<BookOpen className="h-8 w-8" />} title="Засвар байхгүй" description="Транскрипт дээрх үгийг засах эсвэл гараар нэмнэ үү"
            action={<Button size="sm" onClick={openNew}><Plus className="h-4 w-4" />Шинэ засвар</Button>} />
        ) : (
          <Table>
            <THead>
              <tr><TH>Засвар</TH><TH>Дуудлага</TH><TH>Хүрээ</TH><TH>Хэрэглэсэн</TH><TH>Эх ход</TH><TH>Үүсгэсэн</TH><TH className="w-24" /></tr>
            </THead>
            <TBody>
              {items.map((c) => (
                <TR key={c.id}>
                  <TD>
                    <span className="inline-flex items-center gap-2">
                      <span className="text-red-300 line-through decoration-red-300/50">{c.wrong}</span>
                      <ArrowRight className="h-3.5 w-3.5 text-[var(--fg-subtle)]" />
                      <span className="font-medium text-emerald-300">{c.correct}</span>
                    </span>
                  </TD>
                  <TD className="text-[var(--fg-muted)]">{c.phonetic || '—'}</TD>
                  <TD><Badge tone={scopeTone[c.scope]}>{scopeLabel[c.scope]}</Badge></TD>
                  <TD className="tabular-nums">{c.hitCount}</TD>
                  <TD className="font-mono text-xs text-[var(--fg-muted)]" title={c.sourceTurnId ?? undefined}>{c.sourceTurnId ? `${c.sourceTurnId.slice(0, 8)}…` : '—'}</TD>
                  <TD className="whitespace-nowrap text-[var(--fg-muted)]">{fmtDateTime(c.createdAt)}</TD>
                  <TD>
                    <div className="flex justify-end">
                      <Button variant="ghost" size="icon" className="h-8 w-8" aria-label={`Засах ${c.wrong}`} onClick={() => openEdit(c)}><Pencil className="h-4 w-4" /></Button>
                      <Button variant="ghost" size="icon" className="h-8 w-8" aria-label={`Устгах ${c.wrong}`} onClick={() => setToDelete(c)}><Trash2 className="h-4 w-4" /></Button>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <TryPanel />

      <EditDialog open={dialogOpen} item={editing} onClose={() => setDialogOpen(false)} />
      <Dialog open={!!toDelete} onClose={() => setToDelete(null)} title="Засвар устгах"
        description={toDelete ? `${toDelete.wrong} → ${toDelete.correct}` : undefined}
        footer={<>
          <Button variant="ghost" onClick={() => setToDelete(null)}>Болих</Button>
          <Button variant="danger" loading={remove.isPending} onClick={() => toDelete && remove.mutate(toDelete.id)}>Устгах</Button>
        </>}>
        <p className="text-sm text-[var(--fg-muted)]">Энэ засварыг устгахдаа итгэлтэй байна уу?</p>
      </Dialog>
    </div>
  )
}
