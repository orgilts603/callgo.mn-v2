import { useEffect, useRef, useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { Ban, ChevronLeft, ChevronRight, Plus, Search, Trash2, Upload } from 'lucide-react'
import {
  Button, Card, CardBody, CardHeader, Dialog, EmptyState, Field, Input, Skeleton, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import { fmtDateTime, fmtPhone } from '@/lib/utils'
import type { DoNotCallEntry } from '@/lib/types'
import { ConfirmDialog, ErrorNote, errMsg } from './common'
import { useAddDNC, useDeleteDNC, useDNC, useImportDNC, type DNCImportResult } from './hooks'
import { normalizeNumber, validateNumber } from './SIPNumbersTab'

export const DNC_PAGE_SIZE = 25

function AddDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  if (!open) return null
  return <AddForm onClose={onClose} />
}

function AddForm({ onClose }: { onClose: () => void }) {
  const add = useAddDNC()
  const [phone, setPhone] = useState('+976')
  const [reason, setReason] = useState('')
  const [touched, setTouched] = useState(false)
  const phoneError = touched ? validateNumber(phone) : null

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (validateNumber(phone)) return
    add.mutate({ phone: normalizeNumber(phone), reason: reason.trim() || undefined }, {
      onSuccess: () => { toast.success('Хориглох жагсаалтад нэмэгдлээ'); onClose() },
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <Dialog open onClose={onClose} title="Хориглосон дугаар нэмэх" description="Энэ дугаарт кампанит ажил болон гарах дуудлага хийгдэхгүй.">
      <form onSubmit={submit} className="space-y-4" noValidate>
        <Field label="Дугаар" hint="E.164 формат: +976 болон 8 оронтой дугаар (жишээ: +97699112233). 8 оронтой дугаарт +976 автоматаар нэмэгдэнэ." error={phoneError}>
          <Input value={phone} onChange={(e) => setPhone(e.target.value)} onBlur={() => setTouched(true)} placeholder="+97699112233" inputMode="tel" autoFocus />
        </Field>
        <Field label="Шалтгаан" hint="Заавал биш. Жишээ: Харилцагч залгуулахаас татгалзсан">
          <Input value={reason} onChange={(e) => setReason(e.target.value)} />
        </Field>
        <ErrorNote error={add.error} />
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
          <Button type="submit" loading={add.isPending}>Нэмэх</Button>
        </div>
      </form>
    </Dialog>
  )
}

function ImportDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  if (!open) return null
  return <ImportForm onClose={onClose} />
}

function ImportForm({ onClose }: { onClose: () => void }) {
  const imp = useImportDNC()
  const input = useRef<HTMLInputElement>(null)
  const [file, setFile] = useState<File | null>(null)
  const [result, setResult] = useState<DNCImportResult | null>(null)

  const run = () => {
    if (!file) return
    imp.mutate(file, {
      onSuccess: (r) => { setResult(r); toast.success(`${r.imported} дугаар импортлолоо`) },
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <Dialog open onClose={onClose} title="Жагсаалт импортлох" description="CSV эсвэл Excel (.xlsx) файл. Утасны багана автоматаар танигдана."
      footer={result ? <Button onClick={onClose}>Хаах</Button> : (
        <>
          <Button variant="ghost" onClick={onClose}>Болих</Button>
          <Button disabled={!file} loading={imp.isPending} onClick={run}>Импортлох</Button>
        </>
      )}>
      {result ? (
        <div className="space-y-3" data-testid="dnc-import-result">
          <div className="grid grid-cols-3 gap-2 text-center">
            {([['Импортолсон', result.imported], ['Алгассан', result.skipped], ['Алдаа', result.errors?.length ?? 0]] as const).map(([l, v]) => (
              <div key={l} className="rounded-lg border border-[var(--border)] bg-[var(--surface-2)] px-2 py-3">
                <div className="text-xl font-semibold tabular-nums text-[var(--fg)]">{v}</div>
                <div className="text-xs text-[var(--fg-muted)]">{l}</div>
              </div>
            ))}
          </div>
          {(result.errors?.length ?? 0) > 0 && (
            <ul className="max-h-40 overflow-auto rounded-md border border-[var(--border)] px-3 py-2 text-xs text-[var(--fg-muted)]">
              {result.errors?.map((e, i) => <li key={i}><span className="tabular-nums text-[var(--fg-subtle)]">Мөр {e.row}:</span> {e.message}</li>)}
            </ul>
          )}
        </div>
      ) : (
        <div className="space-y-3">
          <button type="button" onClick={() => input.current?.click()}
            className="flex w-full flex-col items-center gap-1.5 rounded-lg border-2 border-dashed border-[var(--border)] px-4 py-6 text-center hover:bg-[var(--surface-2)]">
            <Upload className="h-6 w-6 text-[var(--fg-muted)]" />
            <span className="text-sm text-[var(--fg)]">{file ? file.name : 'CSV эсвэл Excel (.xlsx)'}</span>
            <span className="text-xs text-[var(--fg-muted)]">Файл сонгохын тулд дарна уу</span>
          </button>
          <input ref={input} type="file" accept=".csv,.xlsx,text/csv,application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" className="hidden" aria-label="Импортлох файл"
            onChange={(e) => { const f = e.target.files?.[0]; if (f) setFile(f); e.target.value = '' }} />
          <ErrorNote error={imp.error} />
        </div>
      )}
    </Dialog>
  )
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => { const t = setTimeout(() => setV(value), ms); return () => clearTimeout(t) }, [value, ms])
  return v
}

export default function DoNotCallTab() {
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(0)
  const q = useDebounced(search.trim(), 300)
  const list = useDNC({ q, limit: DNC_PAGE_SIZE, offset: page * DNC_PAGE_SIZE })
  const del = useDeleteDNC()
  const [adding, setAdding] = useState(false)
  const [importing, setImporting] = useState(false)
  const [deleting, setDeleting] = useState<DoNotCallEntry | null>(null)

  const items = list.data?.items ?? []
  const total = list.data?.total ?? 0
  const pages = Math.max(1, Math.ceil(total / DNC_PAGE_SIZE))

  return (
    <div className="space-y-4">
      <Card>
        <CardBody className="space-y-1.5 text-sm text-[var(--fg-muted)]">
          <div className="font-medium text-[var(--fg)]">Яагаад хориглох жагсаалт хэрэгтэй вэ?</div>
          <p><b className="text-[var(--fg)]">Хууль эрх зүй:</b> харилцагч залгуулахаас татгалзсан бол түүнд дахин маркетингийн дуудлага хийх нь хувийн мэдээлэл хамгаалах болон хэрэглэгчийн эрхийн хууль тогтоомжийг зөрчих эрсдэлтэй.</p>
          <p><b className="text-[var(--fg)]">Брэнд:</b> татгалзсан хүнд дахин залгах нь харилцагчийн итгэлийг алдаж, байгууллагын нэр хүндэд сөргөөр нөлөөлнө.</p>
          <p>Энэ жагсаалтад орсон дугаарыг кампанит ажилд импортлохдоо «Алгассан» төлөвтэй болгоно, гараар залгахыг мөн хориглоно.</p>
        </CardBody>
      </Card>

      <Card>
        <CardHeader title="Хориглосон дугаарууд" description={`${total} дугаар`}
          actions={<>
            <div className="relative">
              <Search className="pointer-events-none absolute left-2 top-2 h-4 w-4 text-[var(--fg-subtle)]" />
              <Input value={search} onChange={(e) => { setSearch(e.target.value); setPage(0) }} placeholder="Дугаар, шалтгаан хайх" aria-label="Хайх" className="w-56 pl-8" />
            </div>
            <Button variant="secondary" onClick={() => setImporting(true)}><Upload className="h-4 w-4" />Импортлох</Button>
            <Button onClick={() => setAdding(true)}><Plus className="h-4 w-4" />Дугаар нэмэх</Button>
          </>} />
        {list.isLoading ? (
          <CardBody className="space-y-2"><Skeleton className="h-9" /><Skeleton className="h-9" /><Skeleton className="h-9" /></CardBody>
        ) : list.isError ? (
          <CardBody><ErrorNote error={list.error} /></CardBody>
        ) : items.length === 0 ? (
          <EmptyState icon={<Ban className="h-8 w-8" />} title={q ? 'Хайлтад тохирох дугаар олдсонгүй' : 'Хориглосон дугаар алга'}
            description={q ? undefined : 'Дугаар нэмэх эсвэл CSV/Excel файлаар импортлоорой.'} />
        ) : (
          <Table>
            <THead><TR><TH>Дугаар</TH><TH>Шалтгаан</TH><TH>Нэмсэн огноо</TH><TH className="text-right">Үйлдэл</TH></TR></THead>
            <TBody>
              {items.map((e) => (
                <TR key={e.id}>
                  <TD className="font-mono tabular-nums">{fmtPhone(e.phone)}</TD>
                  <TD className="max-w-72 truncate text-[var(--fg-muted)]" title={e.reason}>{e.reason || '—'}</TD>
                  <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtDateTime(e.createdAt)}</TD>
                  <TD className="text-right">
                    <Button size="icon" variant="ghost" aria-label={`Устгах ${e.phone}`} onClick={() => setDeleting(e)}><Trash2 className="h-4 w-4 text-red-400" /></Button>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
        {total > DNC_PAGE_SIZE && (
          <div className="flex items-center justify-between border-t border-[var(--border)] px-4 py-2 text-xs text-[var(--fg-muted)]">
            <span>{page * DNC_PAGE_SIZE + 1}–{Math.min(total, (page + 1) * DNC_PAGE_SIZE)} / {total}</span>
            <div className="flex items-center gap-1">
              <Button size="sm" variant="ghost" disabled={page === 0} onClick={() => setPage((p) => p - 1)} aria-label="Өмнөх"><ChevronLeft className="h-4 w-4" /></Button>
              <span className="tabular-nums">{page + 1} / {pages}</span>
              <Button size="sm" variant="ghost" disabled={page + 1 >= pages} onClick={() => setPage((p) => p + 1)} aria-label="Дараах"><ChevronRight className="h-4 w-4" /></Button>
            </div>
          </div>
        )}
      </Card>

      <AddDialog open={adding} onClose={() => setAdding(false)} />
      <ImportDialog open={importing} onClose={() => setImporting(false)} />
      <ConfirmDialog
        open={deleting !== null} title="Жагсаалтаас хасах уу?" description={deleting ? `${fmtPhone(deleting.phone)} дугаарт дахин залгах боломжтой болно.` : undefined}
        confirmLabel="Хасах" loading={del.isPending} onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting.phone, {
          onSuccess: () => { toast.success('Жагсаалтаас хасагдлаа'); setDeleting(null) },
          onError: (e) => toast.error(errMsg(e)),
        })}
      />
    </div>
  )
}
