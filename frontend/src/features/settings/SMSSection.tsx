import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { ChevronLeft, ChevronRight, MessageSquare, Send } from 'lucide-react'
import {
  Badge, Button, Card, CardBody, CardHeader, Dialog, EmptyState, Field, Input, Select, Skeleton, Table, TBody, TD, Textarea, TH, THead, TR, type BadgeTone,
} from '@/components/ui'
import type { SMSConfig, SMSMessage } from '@/lib/types'
import { fmtDateTime, fmtPhone } from '@/lib/utils'
import { ErrorNote, errMsg } from './common'
import { FeatureGate } from './FeatureGate'
import { SMS_PAGE_SIZE, useSaveSMSConfig, useSendSMS, useSMSConfig, useSMSMessages, type SMSConfigBody } from './hooks'
import { normalizeNumber, validateNumber } from './SIPNumbersTab'

export const SMS_TEMPLATE_HINT = 'Хувьсагчид: {{name}}, {{phone}}, {{summary}}, {{outcome}}, {{campaign}}, {{vars.X}}'

const msgTone: Record<SMSMessage['status'], BadgeTone> = { queued: 'warning', sent: 'success', failed: 'danger' }
const msgLabel: Record<SMSMessage['status'], string> = { queued: 'Дараалалд', sent: 'Илгээсэн', failed: 'Амжилтгүй' }

export function SMSConfigForm({ config }: { config: SMSConfig }) {
  const save = useSaveSMSConfig()
  const [provider, setProvider] = useState<SMSConfig['provider']>(config.provider || 'mock')
  const [url, setUrl] = useState(config.url ?? '')
  const [apiKey, setApiKey] = useState('')
  const [from, setFrom] = useState(config.from ?? '')
  const [bodyTemplate, setBodyTemplate] = useState(config.bodyTemplate ?? '')
  const [touched, setTouched] = useState(false)

  const urlError = touched && provider === 'http' && !url.trim() ? 'Provider URL оруулна уу' : null

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (provider === 'http' && !url.trim()) return
    const body: SMSConfigBody = {
      provider, from: from.trim(), bodyTemplate: bodyTemplate.trim(),
      ...(provider === 'http' ? { url: url.trim() } : {}),
      ...(provider === 'http' && apiKey ? { apiKey } : {}),
    }
    save.mutate(body, {
      onSuccess: () => { setApiKey(''); toast.success('SMS тохиргоо хадгалагдлаа') },
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Провайдер">
          <Select value={provider} onChange={(e) => setProvider(e.target.value as SMSConfig['provider'])}
            options={[{ value: 'mock', label: 'Mock (туршилт, бодитоор илгээхгүй)' }, { value: 'http', label: 'HTTP gateway' }]} />
        </Field>
        <Field label="Илгээгч (from)" hint="Дугаар эсвэл богино нэр">
          <Input value={from} onChange={(e) => setFrom(e.target.value)} placeholder="CallGo" />
        </Field>
      </div>
      {provider === 'http' && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Provider URL" error={urlError}>
            <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://sms.example.mn/send" inputMode="url" />
          </Field>
          <Field label="API түлхүүр" hint={config.configured ? 'Хадгалагдсан. Хоосон орхивол хэвээр үлдэнэ.' : 'Зөвхөн бичигдэнэ, дахин харагдахгүй.'}>
            <Input type="password" autoComplete="off" value={apiKey} onChange={(e) => setApiKey(e.target.value)} placeholder={config.configured ? '••••••••' : ''} />
          </Field>
        </div>
      )}
      <Field label="Мессежийн загвар" hint={SMS_TEMPLATE_HINT}>
        <Textarea rows={3} value={bodyTemplate} onChange={(e) => setBodyTemplate(e.target.value)} placeholder="Сайн байна уу {{name}}, ..." />
      </Field>
      <ErrorNote error={save.error} />
      <div className="flex justify-end"><Button type="submit" loading={save.isPending}>Хадгалах</Button></div>
    </form>
  )
}

export function TestSMSDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  if (!open) return null
  return <TestSMSForm onClose={onClose} />
}

function TestSMSForm({ onClose }: { onClose: () => void }) {
  const send = useSendSMS()
  const [to, setTo] = useState('+976')
  const [body, setBody] = useState('')
  const [touched, setTouched] = useState(false)
  const [result, setResult] = useState<SMSMessage | null>(null)
  const toError = touched ? validateNumber(to) : null
  const bodyError = touched && !body.trim() ? 'Мессеж оруулна уу' : null

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (validateNumber(to) || !body.trim()) return
    send.mutate({ to: normalizeNumber(to), body: body.trim() }, {
      onSuccess: (res) => setResult(res.message),
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <Dialog open onClose={onClose} title="Тест SMS" description="Тохиргоог шалгахын тулд нэг мессеж илгээнэ (SMS квот хасагдана).">
      <form onSubmit={submit} className="space-y-4" noValidate>
        <Field label="Хүлээн авагч" error={toError} hint="+976 болон 8 оронтой дугаар">
          <Input value={to} onChange={(e) => setTo(e.target.value)} inputMode="tel" autoFocus />
        </Field>
        <Field label="Мессеж" error={bodyError}>
          <Textarea rows={3} value={body} onChange={(e) => setBody(e.target.value)} />
        </Field>
        {result && (
          <div role="status" data-testid="sms-result" className="flex items-center gap-2 text-sm">
            <Badge tone={msgTone[result.status]} dot>{msgLabel[result.status]}</Badge>
            {result.error && <span className="text-xs text-[var(--danger)]">{result.error}</span>}
          </div>
        )}
        <ErrorNote error={send.error} />
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>Хаах</Button>
          <Button type="submit" loading={send.isPending}><Send className="h-4 w-4" />Илгээх</Button>
        </div>
      </form>
    </Dialog>
  )
}

function SentList() {
  const [offset, setOffset] = useState(0)
  const q = useSMSMessages(offset)
  const items = q.data?.items ?? []
  const total = q.data?.total ?? 0
  if (q.isLoading) return <CardBody><Skeleton className="h-9" /></CardBody>
  if (q.isError) return <CardBody><ErrorNote error={q.error} /></CardBody>
  if (items.length === 0) return <EmptyState icon={<MessageSquare className="h-8 w-8" />} title="Илгээсэн SMS алга" />
  return (
    <>
      <Table>
        <THead><TR><TH>Хүлээн авагч</TH><TH>Мессеж</TH><TH>Төлөв</TH><TH>Провайдер</TH><TH>Огноо</TH></TR></THead>
        <TBody>
          {items.map((m) => (
            <TR key={m.id}>
              <TD className="whitespace-nowrap font-mono tabular-nums">{fmtPhone(m.to)}</TD>
              <TD className="max-w-xs truncate" title={m.body}>{m.body}</TD>
              <TD><Badge tone={msgTone[m.status]} dot title={m.error}>{msgLabel[m.status]}</Badge></TD>
              <TD className="text-[var(--fg-muted)]">{m.provider}</TD>
              <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtDateTime(m.sentAt ?? m.createdAt)}</TD>
            </TR>
          ))}
        </TBody>
      </Table>
      <div className="flex items-center justify-between border-t border-[var(--border-subtle)] px-3 py-2 text-xs text-[var(--fg-muted)]">
        <span className="tabular-nums">{offset + 1}–{offset + items.length} / {total}</span>
        <div className="flex gap-1">
          <Button size="icon" variant="ghost" aria-label="Өмнөх SMS" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - SMS_PAGE_SIZE))}><ChevronLeft className="h-4 w-4" /></Button>
          <Button size="icon" variant="ghost" aria-label="Дараах SMS" disabled={offset + items.length >= total} onClick={() => setOffset(offset + SMS_PAGE_SIZE)}><ChevronRight className="h-4 w-4" /></Button>
        </div>
      </div>
    </>
  )
}

export function SMSSection() {
  const config = useSMSConfig()
  const [testing, setTesting] = useState(false)
  return (
    <section aria-labelledby="sms-heading" className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="sms-heading" className="text-sm font-semibold text-[var(--fg)]">SMS</h2>
          <p className="mt-1 max-w-3xl text-sm text-[var(--fg-muted)]">Дуудлагын дараа автоматаар SMS илгээх болон гараар тест илгээх тохиргоо.</p>
        </div>
        {config.data && <Button variant="outline" onClick={() => setTesting(true)}><Send className="h-4 w-4" />Тест SMS</Button>}
      </div>
      <FeatureGate error={config.error} feature="SMS">
        {config.isLoading ? (
          <Card><CardBody><Skeleton className="h-32" /></CardBody></Card>
        ) : config.isError ? (
          <ErrorNote error={config.error} />
        ) : config.data ? (
          <>
            <Card>
              <CardHeader title="Тохиргоо" actions={config.data.configured ? <Badge tone="success" dot>Тохируулсан</Badge> : <Badge tone="warning" dot>Тохируулаагүй</Badge>} />
              <CardBody><SMSConfigForm key={`${config.data.provider}|${config.data.url ?? ''}|${config.data.from}|${config.data.bodyTemplate ?? ''}`} config={config.data} /></CardBody>
            </Card>
            <Card>
              <CardHeader title="Илгээсэн мессежүүд" />
              <SentList />
            </Card>
          </>
        ) : null}
      </FeatureGate>
      <TestSMSDialog open={testing} onClose={() => setTesting(false)} />
    </section>
  )
}
