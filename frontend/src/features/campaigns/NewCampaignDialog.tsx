import { useCallback, useMemo, useRef, useState, type DragEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2, ChevronDown, ChevronRight, Download, FileSpreadsheet, UploadCloud } from 'lucide-react'
import { toast } from 'sonner'
import { Badge, Button, Dialog, Field, Input, Select, Table, TBody, TD, TH, THead, TR, Textarea } from '@/components/ui'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'
import { parseCsvFile, downloadTemplate, type CsvPreview } from './csv'
import { campaignKeys, useAgentProfiles, useSipNumbers, type CreateCampaignResult } from './hooks'

export interface CampaignFormValues {
  name: string
  script: string
  sipNumberId: string
  agentProfileId: string
  concurrency: number
  maxAttempts: number
}

export const defaultValues: CampaignFormValues = { name: '', script: '', sipNumberId: '', agentProfileId: '', concurrency: 2, maxAttempts: 2 }

export function buildCampaignFormData(v: CampaignFormValues, file: File): FormData {
  const fd = new FormData()
  fd.append('name', v.name.trim())
  fd.append('script', v.script)
  fd.append('sipNumberId', v.sipNumberId)
  fd.append('agentProfileId', v.agentProfileId)
  fd.append('concurrency', String(v.concurrency))
  fd.append('maxAttempts', String(v.maxAttempts))
  fd.append('file', file)
  return fd
}

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.round(n) || lo))

const STEPS = ['Тохиргоо', 'Жагсаалт', 'Үр дүн']

function Stepper({ step }: { step: number }) {
  return (
    <ol className="mb-5 flex items-center gap-2 text-xs">
      {STEPS.map((label, i) => (
        <li key={label} className="flex items-center gap-2">
          <span className={cn('flex h-5 w-5 items-center justify-center rounded-full border text-[10px] font-semibold',
            i + 1 === step ? 'border-[var(--accent)] bg-[var(--accent)] text-white'
              : i + 1 < step ? 'border-emerald-500/50 text-emerald-400' : 'border-[var(--border)] text-[var(--fg-subtle)]')}>{i + 1}</span>
          <span className={i + 1 === step ? 'font-medium text-[var(--fg)]' : 'text-[var(--fg-muted)]'}>{label}</span>
          {i < STEPS.length - 1 && <span className="h-px w-6 bg-[var(--border)]" />}
        </li>
      ))}
    </ol>
  )
}

export function CsvPreviewTable({ preview }: { preview: CsvPreview }) {
  const role = (col: string) => (col === preview.phoneColumn ? 'phone' : col === preview.nameColumn ? 'name' : null)
  return (
    <div data-testid="csv-preview" className="space-y-2">
      <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--fg-muted)]">
        <span><b className="text-[var(--fg)]">{preview.rowCount}</b> мөр</span>
        <span>·</span>
        <span>Баганууд: {preview.columns.join(', ') || '—'}</span>
      </div>
      {preview.phoneColumn ? (
        <div className="flex items-center gap-1.5 text-xs text-emerald-400" data-testid="phone-detected">
          <CheckCircle2 className="h-3.5 w-3.5" />Утасны багана: <b>{preview.phoneColumn}</b>
          {preview.nameColumn && <span className="text-[var(--fg-muted)]">· Нэр: <b>{preview.nameColumn}</b></span>}
        </div>
      ) : (
        <div className="flex items-center gap-1.5 text-xs text-red-400" data-testid="phone-missing">
          <AlertTriangle className="h-3.5 w-3.5" />Утасны багана олдсонгүй (phone, утас, дугаар, mobile, tel).
        </div>
      )}
      <div className="max-h-56 overflow-auto rounded-md border border-[var(--border)]">
        <Table>
          <THead>
            <tr>
              {preview.columns.map((c) => {
                const r = role(c)
                return (
                  <TH key={c} className={cn(r && 'bg-[var(--accent)]/10 text-[var(--fg)]')}>
                    <div className="flex items-center gap-1.5 normal-case">
                      {c}
                      {r === 'phone' && <Badge tone="accent">Утас</Badge>}
                      {r === 'name' && <Badge tone="info">Нэр</Badge>}
                    </div>
                  </TH>
                )
              })}
            </tr>
          </THead>
          <TBody>
            {preview.previewRows.map((row, i) => (
              <TR key={i}>
                {preview.columns.map((c) => (
                  <TD key={c} className={cn('whitespace-nowrap text-xs', role(c) && 'bg-[var(--accent)]/5')}>{row[c] ?? ''}</TD>
                ))}
              </TR>
            ))}
          </TBody>
        </Table>
      </div>
      {preview.rowCount > preview.previewRows.length && (
        <div className="text-[11px] text-[var(--fg-subtle)]">Эхний {preview.previewRows.length} мөрийг харуулав.</div>
      )}
    </div>
  )
}

function DropZone({ file, onFile }: { file: File | null; onFile: (f: File) => void }) {
  const input = useRef<HTMLInputElement>(null)
  const [over, setOver] = useState(false)
  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setOver(false)
    const f = e.dataTransfer.files?.[0]
    if (f) onFile(f)
  }
  return (
    <div
      onDragOver={(e) => { e.preventDefault(); setOver(true) }}
      onDragLeave={() => setOver(false)}
      onDrop={onDrop}
      onClick={() => input.current?.click()}
      data-testid="dropzone"
      className={cn('flex cursor-pointer flex-col items-center gap-1.5 rounded-lg border-2 border-dashed px-4 py-6 text-center transition-colors',
        over ? 'border-[var(--accent)] bg-[var(--accent)]/10' : 'border-[var(--border)] hover:bg-[var(--surface-2)]')}>
      {file ? <FileSpreadsheet className="h-6 w-6 text-[var(--accent)]" /> : <UploadCloud className="h-6 w-6 text-[var(--fg-muted)]" />}
      <div className="text-sm text-[var(--fg)]">{file ? file.name : 'CSV файлаа энд чирж оруулна уу'}</div>
      <div className="text-xs text-[var(--fg-muted)]">{file ? 'Өөр файл сонгохын тулд дарна уу' : 'эсвэл дарж файл сонгоно уу'}</div>
      <input ref={input} type="file" accept=".csv,text/csv" className="hidden" aria-label="CSV файл"
        onChange={(e) => { const f = e.target.files?.[0]; if (f) onFile(f); e.target.value = '' }} />
    </div>
  )
}

function ResultView({ result }: { result: CreateCampaignResult }) {
  const [showErrors, setShowErrors] = useState(false)
  const { imported, skipped, errors } = result.targets
  return (
    <div className="space-y-3" data-testid="result">
      <div className="flex items-center gap-2 text-sm font-medium text-emerald-400"><CheckCircle2 className="h-4 w-4" />«{result.campaign.name}» үүслээ</div>
      <div className="grid grid-cols-3 gap-2 text-center">
        {[['Импортолсон', imported], ['Алгассан', skipped], ['Алдаа', errors?.length ?? 0]].map(([l, v]) => (
          <div key={l} className="rounded-lg border border-[var(--border)] bg-[var(--surface-2)] px-2 py-3">
            <div className="text-xl font-semibold tabular-nums text-[var(--fg)]">{v}</div>
            <div className="text-xs text-[var(--fg-muted)]">{l}</div>
          </div>
        ))}
      </div>
      {errors?.length > 0 && (
        <div className="rounded-lg border border-[var(--border)]">
          <button type="button" onClick={() => setShowErrors((s) => !s)} aria-expanded={showErrors}
            className="flex w-full items-center gap-1.5 px-3 py-2 text-xs font-medium text-[var(--fg)]">
            {showErrors ? <ChevronDown className="h-3.5 w-3.5" /> : <ChevronRight className="h-3.5 w-3.5" />}
            Алдааны жагсаалт ({errors.length})
          </button>
          {showErrors && (
            <ul className="max-h-40 overflow-auto border-t border-[var(--border)] px-3 py-2 text-xs text-[var(--fg-muted)]">
              {errors.map((e, i) => <li key={i}><span className="tabular-nums text-[var(--fg-subtle)]">Мөр {e.row}:</span> {e.message}</li>)}
            </ul>
          )}
        </div>
      )}
    </div>
  )
}

export function NewCampaignDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const sips = useSipNumbers()
  const profiles = useAgentProfiles()
  const [step, setStep] = useState(1)
  const [values, setValues] = useState<CampaignFormValues>(defaultValues)
  const [file, setFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<CsvPreview | null>(null)
  const [parseError, setParseError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState('')
  const [result, setResult] = useState<CreateCampaignResult | null>(null)

  const set = <K extends keyof CampaignFormValues>(k: K, v: CampaignFormValues[K]) => setValues((s) => ({ ...s, [k]: v }))

  const outbound = useMemo(() => (sips.data ?? []).filter((s) => s.allowOutbound && s.active), [sips.data])
  const sipOptions = outbound.map((s) => ({ value: s.id, label: s.label ? `${s.label} (${s.number})` : s.number }))
  const profileOptions = (profiles.data ?? []).map((p) => ({ value: p.id, label: p.name }))

  const step1Valid = values.name.trim() !== '' && values.sipNumberId !== '' && values.agentProfileId !== ''
  const step2Valid = !!file && !!preview?.phoneColumn && preview.rowCount > 0

  const reset = useCallback(() => {
    setStep(1); setValues(defaultValues); setFile(null); setPreview(null); setParseError('')
    setSubmitting(false); setSubmitError(''); setResult(null)
  }, [])
  const close = () => { onClose(); reset() }

  const onSip = (id: string) => {
    const sip = outbound.find((s) => s.id === id)
    setValues((s) => ({ ...s, sipNumberId: id, agentProfileId: s.agentProfileId || sip?.agentProfileId || '' }))
  }

  const onFile = async (f: File) => {
    setFile(f); setPreview(null); setParseError('')
    try { setPreview(await parseCsvFile(f)) } catch { setParseError('Файлыг уншиж чадсангүй') }
  }

  const submit = async () => {
    if (!file) return
    setSubmitting(true); setSubmitError('')
    try {
      const res = await api.post<CreateCampaignResult>('/campaigns', buildCampaignFormData(values, file))
      setResult(res)
      setStep(3)
      void qc.invalidateQueries({ queryKey: campaignKeys.all })
      toast.success('Кампанит ажил үүслээ')
    } catch (e) {
      setSubmitError(e instanceof Error ? e.message : 'Алдаа гарлаа')
    } finally { setSubmitting(false) }
  }

  const footer = step === 1 ? (
    <>
      <Button variant="ghost" onClick={close}>Болих</Button>
      <Button disabled={!step1Valid} onClick={() => setStep(2)}>Үргэлжлүүлэх</Button>
    </>
  ) : step === 2 ? (
    <>
      <Button variant="ghost" onClick={() => setStep(1)} disabled={submitting}>Буцах</Button>
      <Button disabled={!step2Valid} loading={submitting} onClick={() => void submit()}>Үүсгэх</Button>
    </>
  ) : (
    <>
      <Button variant="ghost" onClick={close}>Хаах</Button>
      <Button onClick={() => { const id = result?.campaign.id; close(); if (id) navigate(`/campaigns/${id}`) }}>Дэлгэрэнгүй үзэх</Button>
    </>
  )

  return (
    <Dialog open={open} onClose={close} title="Шинэ кампанит ажил" className="max-w-2xl" footer={footer}>
      <Stepper step={step} />
      {step === 1 && (
        <div className="space-y-4">
          <Field label="Нэр"><Input value={values.name} onChange={(e) => set('name', e.target.value)} placeholder="Жишээ: Долдугаар сарын сануулга" /></Field>
          <Field label="Скрипт" hint="{{name}} гэх мэт CSV баганыг хувьсагчаар ашиглаж болно">
            <Textarea rows={4} value={values.script} onChange={(e) => set('script', e.target.value)} placeholder="Сайн байна уу {{name}}, ..." />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="SIP дугаар" hint={!sips.isLoading && outbound.length === 0 ? 'Гарах дуудлага зөвшөөрсөн идэвхтэй дугаар алга' : undefined}>
              <Select value={values.sipNumberId} onChange={(e) => onSip(e.target.value)} options={sipOptions} placeholder="Сонгох…" />
            </Field>
            <Field label="Агент профайл">
              <Select value={values.agentProfileId} onChange={(e) => set('agentProfileId', e.target.value)} options={profileOptions} placeholder="Сонгох…" />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Field label="Зэрэг дуудлага (1-50)">
                <Input type="number" min={1} max={50} value={values.concurrency}
                  onChange={(e) => set('concurrency', clamp(Number(e.target.value), 1, 50))} />
              </Field>
              <input type="range" min={1} max={50} value={values.concurrency} aria-label="Зэрэг дуудлагын слайдер"
                onChange={(e) => set('concurrency', Number(e.target.value))} className="w-full accent-[var(--accent)]" />
            </div>
            <Field label="Дахин оролдох тоо (1-5)">
              <Input type="number" min={1} max={5} value={values.maxAttempts}
                onChange={(e) => set('maxAttempts', clamp(Number(e.target.value), 1, 5))} />
            </Field>
          </div>
        </div>
      )}
      {step === 2 && (
        <div className="space-y-4">
          <DropZone file={file} onFile={(f) => void onFile(f)} />
          <button type="button" onClick={downloadTemplate} className="inline-flex items-center gap-1.5 text-xs text-[var(--accent)] hover:underline">
            <Download className="h-3.5 w-3.5" />Загвар татах (phone,name,note)
          </button>
          {parseError && <div className="text-xs text-red-400">{parseError}</div>}
          {preview && <CsvPreviewTable preview={preview} />}
          {submitError && <div role="alert" className="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">{submitError}</div>}
        </div>
      )}
      {step === 3 && result && <ResultView result={result} />}
    </Dialog>
  )
}
