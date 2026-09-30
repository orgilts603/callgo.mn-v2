import { useEffect, useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronLeft, ChevronRight, Plus, Search, Trash2, Upload, Users } from 'lucide-react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { fmtDateTime, fmtPhone } from '@/lib/utils'
import type { Contact, ListResponse } from '@/lib/types'
import {
  Badge, Button, Card, Dialog, EmptyState, Field, Input, PageHeader, Skeleton, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import { normalizePhone, parseTags } from './phone'
import { useDebounced } from '../history/useDebounced'

export const CONTACTS_PAGE_SIZE = 25

interface ImportResult { imported: number; skipped: number; errors: { row: number; message: string }[] }

export function NewContactDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const [phone, setPhone] = useState('')
  const [name, setName] = useState('')
  const [tags, setTags] = useState('')
  const [touched, setTouched] = useState(false)
  const normalized = normalizePhone(phone)

  useEffect(() => { if (open) { setPhone(''); setName(''); setTags(''); setTouched(false) } }, [open])

  const create = useMutation({
    mutationFn: (body: { phone: string; name: string; tags: string[] }) => api.post<{ contact: Contact }>('/contacts', body),
    onSuccess: () => {
      toast.success('Харилцагч хадгалагдлаа')
      void qc.invalidateQueries({ queryKey: ['contacts'] })
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!normalized) return
    create.mutate({ phone: normalized, name: name.trim(), tags: parseTags(tags) })
  }

  return (
    <Dialog open={open} onClose={onClose} title="Шинэ харилцагч"
      footer={<>
        <Button variant="ghost" type="button" onClick={onClose}>Болих</Button>
        <Button type="submit" form="new-contact-form" loading={create.isPending}>Хадгалах</Button>
      </>}>
      <form id="new-contact-form" onSubmit={submit} className="space-y-4">
        <Field label="Утасны дугаар" hint="E.164 формат, жишээ нь +97699112233 (8 оронтой дугаарт +976 автоматаар нэмэгдэнэ)"
          error={touched && !normalized ? 'Утасны дугаар буруу байна' : undefined}>
          <Input value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="+976 9911 2233" inputMode="tel" autoFocus />
        </Field>
        <Field label="Нэр">
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Бат-Эрдэнэ" />
        </Field>
        <Field label="Шошго" hint="Таслалаар тусгаарлана">
          <Input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="vip, шинэ" />
        </Field>
      </form>
    </Dialog>
  )
}

function ImportDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient()
  const [file, setFile] = useState<File | null>(null)
  const [result, setResult] = useState<ImportResult | null>(null)

  useEffect(() => { if (open) { setFile(null); setResult(null) } }, [open])

  const upload = useMutation({
    mutationFn: (f: File) => {
      const fd = new FormData()
      fd.append('file', f)
      return api.post<ImportResult>('/contacts/import', fd)
    },
    onSuccess: (r) => {
      setResult(r)
      void qc.invalidateQueries({ queryKey: ['contacts'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Dialog open={open} onClose={onClose} title="CSV импорт" description="Заавал phone багана, нэмэлтээр name, tags багана байж болно"
      footer={<>
        <Button variant="ghost" onClick={onClose}>{result ? 'Хаах' : 'Болих'}</Button>
        {!result && <Button disabled={!file} loading={upload.isPending} onClick={() => file && upload.mutate(file)}>Импорт хийх</Button>}
      </>}>
      {result ? (
        <div className="space-y-3 text-sm">
          <div className="flex gap-3">
            <Badge tone="success">Импортолсон: {result.imported}</Badge>
            <Badge tone="warning">Алгассан: {result.skipped}</Badge>
          </div>
          {result.errors?.length > 0 && (
            <ul className="max-h-48 space-y-1 overflow-y-auto rounded-md border border-[var(--border)] p-3 text-xs">
              {result.errors.map((er, i) => <li key={i} className="text-red-300">Мөр {er.row}: {er.message}</li>)}
            </ul>
          )}
        </div>
      ) : (
        <Field label="CSV файл">
          <input type="file" accept=".csv,text/csv" aria-label="CSV файл" onChange={(e) => setFile(e.target.files?.[0] ?? null)}
            className="block w-full text-sm text-[var(--fg-muted)] file:mr-3 file:cursor-pointer file:rounded-md file:border file:border-[var(--border)] file:bg-[var(--surface-2)] file:px-3 file:py-1.5 file:text-xs file:text-[var(--fg)]" />
        </Field>
      )}
    </Dialog>
  )
}

export function ContactsPage() {
  const qc = useQueryClient()
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(0)
  const [newOpen, setNewOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [toDelete, setToDelete] = useState<Contact | null>(null)
  const q = useDebounced(search.trim(), 300)
  useEffect(() => { setPage(0) }, [q])

  const list = useQuery({
    queryKey: ['contacts', 'list', q, page],
    queryFn: () => api.get<ListResponse<Contact>>('/contacts', { q: q || undefined, limit: CONTACTS_PAGE_SIZE, offset: page * CONTACTS_PAGE_SIZE }),
    placeholderData: keepPreviousData,
  })

  const remove = useMutation({
    mutationFn: (id: string) => api.delete(`/contacts/${id}`),
    onSuccess: () => {
      toast.success('Харилцагч устгагдлаа')
      setToDelete(null)
      void qc.invalidateQueries({ queryKey: ['contacts'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const items = list.data?.items ?? []
  const total = list.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / CONTACTS_PAGE_SIZE))

  return (
    <div>
      <PageHeader title="Харилцагчид" description="Дуудлага хийх болон хүлээн авах харилцагчдын жагсаалт"
        actions={<>
          <Button variant="secondary" size="sm" onClick={() => setImportOpen(true)}><Upload className="h-4 w-4" />CSV импорт</Button>
          <Button size="sm" onClick={() => setNewOpen(true)}><Plus className="h-4 w-4" />Шинэ харилцагч</Button>
        </>} />

      <Card className="mb-4 p-4">
        <div className="relative max-w-md">
          <Search className="pointer-events-none absolute left-3 top-2.5 h-4 w-4 text-[var(--fg-subtle)]" />
          <Input className="pl-9" placeholder="Утас эсвэл нэрээр хайх" aria-label="Хайх" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
      </Card>

      <Card>
        {list.isLoading ? (
          <div className="space-y-2 p-4">{Array.from({ length: 6 }, (_, i) => <Skeleton key={i} className="h-9 w-full" />)}</div>
        ) : list.isError ? (
          <EmptyState title="Харилцагч ачаалж чадсангүй" description={(list.error as Error).message} />
        ) : items.length === 0 ? (
          <EmptyState icon={<Users className="h-8 w-8" />} title="Харилцагч олдсонгүй"
            description={q ? 'Хайлтаа өөрчилж үзнэ үү' : 'Шинэ харилцагч нэмэх эсвэл CSV импортлоно уу'} />
        ) : (
          <Table>
            <THead>
              <tr><TH>Утас</TH><TH>Нэр</TH><TH>Шошго</TH><TH>Үүсгэсэн</TH><TH>Шинэчилсэн</TH><TH className="w-10" /></tr>
            </THead>
            <TBody>
              {items.map((c) => (
                <TR key={c.id}>
                  <TD className="whitespace-nowrap tabular-nums"><Link className="text-[var(--accent-fg)] hover:underline" to={`/contacts/${c.id}`}>{fmtPhone(c.phone)}</Link></TD>
                  <TD>{c.name || '—'}</TD>
                  <TD><div className="flex flex-wrap gap-1">{c.tags?.length ? c.tags.map((t) => <Badge key={t}>{t}</Badge>) : <span className="text-[var(--fg-subtle)]">—</span>}</div></TD>
                  <TD className="whitespace-nowrap text-[var(--fg-muted)]">{fmtDateTime(c.createdAt)}</TD>
                  <TD className="whitespace-nowrap text-[var(--fg-muted)]">{fmtDateTime(c.updatedAt)}</TD>
                  <TD>
                    <Button variant="ghost" size="icon" className="h-8 w-8" aria-label={`Устгах ${c.phone}`} onClick={() => setToDelete(c)}><Trash2 className="h-4 w-4" /></Button>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
        <div className="flex items-center justify-between border-t border-[var(--border)] px-4 py-3 text-xs text-[var(--fg-muted)]">
          <span>Нийт {total} харилцагч</span>
          <div className="flex items-center gap-2">
            <Button variant="outline" size="icon" className="h-8 w-8" aria-label="Өмнөх" disabled={page === 0} onClick={() => setPage((p) => Math.max(0, p - 1))}><ChevronLeft className="h-4 w-4" /></Button>
            <span className="tabular-nums">{page + 1} / {pages}</span>
            <Button variant="outline" size="icon" className="h-8 w-8" aria-label="Дараах" disabled={page + 1 >= pages} onClick={() => setPage((p) => p + 1)}><ChevronRight className="h-4 w-4" /></Button>
          </div>
        </div>
      </Card>

      <NewContactDialog open={newOpen} onClose={() => setNewOpen(false)} />
      <ImportDialog open={importOpen} onClose={() => setImportOpen(false)} />
      <Dialog open={!!toDelete} onClose={() => setToDelete(null)} title="Харилцагч устгах"
        description={toDelete ? `${fmtPhone(toDelete.phone)} ${toDelete.name}`.trim() : undefined}
        footer={<>
          <Button variant="ghost" onClick={() => setToDelete(null)}>Болих</Button>
          <Button variant="danger" loading={remove.isPending} onClick={() => toDelete && remove.mutate(toDelete.id)}>Устгах</Button>
        </>}>
        <p className="text-sm text-[var(--fg-muted)]">Энэ харилцагчийг устгахдаа итгэлтэй байна уу? Энэ үйлдлийг буцаах боломжгүй.</p>
      </Dialog>
    </div>
  )
}
