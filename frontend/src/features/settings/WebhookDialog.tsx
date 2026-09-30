import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { Check, Copy, KeyRound } from 'lucide-react'
import { Button, Dialog, Field, Input } from '@/components/ui'
import type { Webhook } from '@/lib/types'
import { ErrorNote, errMsg } from './common'
import { useCreateWebhook, useUpdateWebhook, type WebhookCreateBody } from './hooks'

export const WEBHOOK_EVENTS: { id: string; label: string }[] = [
  { id: 'call.started', label: 'Дуудлага эхэлсэн' },
  { id: 'call.ringing', label: 'Дуугарч байна' },
  { id: 'call.answered', label: 'Хариулсан' },
  { id: 'call.ended', label: 'Дуудлага дууссан' },
  { id: 'call.updated', label: 'Дуудлага шинэчлэгдсэн' },
  { id: 'transcript.final', label: 'Ярианы текст (эцсийн)' },
  { id: 'transcript.partial', label: 'Ярианы текст (түр)' },
  { id: 'agent.state', label: 'Агентын төлөв' },
  { id: 'campaign.progress', label: 'Кампанит ажлын явц' },
  { id: 'callback.scheduled', label: 'Буцаж залгах товлосон' },
  { id: 'quota.warning', label: 'Квотын сануулга' },
  { id: 'billing.updated', label: 'Төлбөр шинэчлэгдсэн' },
  { id: 'webhook.failed', label: 'Webhook амжилтгүй' },
  { id: 'lexicon.updated', label: 'Толь шинэчлэгдсэн' },
  { id: 'system', label: 'Системийн (тест)' },
]

export function validateWebhookUrl(raw: string): string | null {
  const v = raw.trim()
  if (!v) return 'URL оруулна уу'
  try {
    const u = new URL(v)
    if (u.protocol !== 'https:' && u.protocol !== 'http:') return 'http:// эсвэл https:// URL байх ёстой'
  } catch {
    return 'URL буруу байна'
  }
  return null
}

/** One-time secret display with a copy button. */
export function SecretPanel({ secret, onDone, title = 'Webhook secret' }: { secret: string; onDone: () => void; title?: string }) {
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      await navigator.clipboard.writeText(secret)
      setCopied(true)
    } catch {
      toast.error('Хуулж чадсангүй. Гараар хуулна уу.')
    }
  }
  return (
    <div className="space-y-4" data-testid="secret-panel">
      <div role="alert" className="rounded-md border border-[var(--warning-border)] bg-[var(--warning-soft)] px-3 py-2 text-xs text-[var(--warning-fg)]">
        {title}-ийг зөвхөн одоо харах боломжтой. Хаасны дараа дахин харуулахгүй тул аюулгүй газарт хадгална уу.
      </div>
      <div className="flex items-center gap-2">
        <code data-testid="secret-value" className="min-w-0 flex-1 break-all rounded-md border border-[var(--border)] bg-[var(--surface-inset)] px-3 py-2 font-mono text-xs text-[var(--fg)]">{secret}</code>
        <Button type="button" variant="outline" onClick={() => void copy()} aria-label="Secret хуулах">
          {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}{copied ? 'Хуулсан' : 'Хуулах'}
        </Button>
      </div>
      <div className="flex justify-end"><Button type="button" onClick={onDone}>Хадгалсан, хаах</Button></div>
    </div>
  )
}

export function RotatedSecretDialog({ secret, onClose }: { secret: string | null; onClose: () => void }) {
  return (
    <Dialog open={secret !== null} onClose={onClose} title="Шинэ secret үүслээ" description="Хуучин secret хүчингүй боллоо. Хүлээн авагч талдаа шинэчилнэ үү.">
      {secret !== null && <SecretPanel secret={secret} onDone={onClose} />}
    </Dialog>
  )
}

export function WebhookDialog({ open, onClose, initial }: { open: boolean; onClose: () => void; initial?: Webhook | null }) {
  if (!open) return null
  return <WebhookForm key={initial?.id ?? 'new'} initial={initial} onClose={onClose} />
}

function WebhookForm({ initial, onClose }: { initial?: Webhook | null; onClose: () => void }) {
  const create = useCreateWebhook()
  const update = useUpdateWebhook()
  const [url, setUrl] = useState(initial?.url ?? '')
  const [description, setDescription] = useState(initial?.description ?? '')
  const [events, setEvents] = useState<string[]>(initial?.events ?? ['call.ended'])
  const [touched, setTouched] = useState(false)
  const [secret, setSecret] = useState<string | null>(null)

  const all = events.includes('*')
  const urlError = touched ? validateWebhookUrl(url) : null
  const eventsError = touched && events.length === 0 ? 'Дор хаяж нэг үйл явдал сонгоно уу' : null

  const toggle = (id: string) => setEvents((s) => (s.includes(id) ? s.filter((e) => e !== id) : [...s, id]))
  const toggleAll = () => setEvents(all ? [] : ['*'])

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (validateWebhookUrl(url) || events.length === 0) return
    const body: WebhookCreateBody = { url: url.trim(), events: all ? ['*'] : events, description: description.trim() }
    if (initial) {
      update.mutate({ id: initial.id, body }, {
        onSuccess: () => { toast.success('Webhook шинэчлэгдлээ'); onClose() },
        onError: (err) => toast.error(errMsg(err)),
      })
    } else {
      create.mutate(body, {
        onSuccess: (res) => setSecret(res.secret),
        onError: (err) => toast.error(errMsg(err)),
      })
    }
  }

  if (secret !== null) {
    return (
      <Dialog open onClose={onClose} title="Webhook үүслээ" description="Хүсэлтийн гарын үсгийг шалгахад доорх secret хэрэгтэй.">
        <SecretPanel secret={secret} onDone={onClose} />
      </Dialog>
    )
  }

  const pending = create.isPending || update.isPending
  return (
    <Dialog open onClose={onClose} title={initial ? 'Webhook засах' : 'Webhook нэмэх'}
      description="Сонгосон үйл явдал болоход энэ URL руу гарын үсэгтэй JSON илгээнэ.">
      <form onSubmit={submit} className="space-y-4" noValidate>
        <Field label="URL" error={urlError}>
          <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/hooks/callgo" inputMode="url" autoFocus />
        </Field>
        <Field label="Тайлбар" hint="Заавал биш">
          <Input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="CRM интеграц" />
        </Field>
        <fieldset className="space-y-2">
          <legend className="text-xs font-medium text-[var(--fg-muted)]">Үйл явдлууд</legend>
          <label className="flex cursor-pointer items-center gap-2 rounded-md border border-[var(--border)] bg-[var(--surface-0)] px-3 py-1.5 text-sm">
            <input type="checkbox" className="h-4 w-4 accent-[var(--accent)]" checked={all} onChange={toggleAll} />
            <span className="font-mono text-xs">*</span><span className="text-[var(--fg-muted)]">Бүх үйл явдал</span>
          </label>
          <div className="grid max-h-56 gap-1 overflow-y-auto sm:grid-cols-2">
            {WEBHOOK_EVENTS.map((ev) => (
              <label key={ev.id} className={`flex cursor-pointer items-center gap-2 rounded-md px-2 py-1 text-xs hover:bg-[var(--surface-2)] ${all ? 'opacity-50' : ''}`}>
                <input type="checkbox" className="h-4 w-4 accent-[var(--accent)]" disabled={all} checked={all || events.includes(ev.id)} onChange={() => toggle(ev.id)} />
                <span><span className="block font-mono">{ev.id}</span><span className="block text-[var(--fg-subtle)]">{ev.label}</span></span>
              </label>
            ))}
          </div>
          {eventsError && <div role="alert" className="text-xs text-[var(--danger)]">{eventsError}</div>}
        </fieldset>
        <ErrorNote error={create.error ?? update.error} />
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
          <Button type="submit" loading={pending}>{!initial && <KeyRound className="h-4 w-4" />}{initial ? 'Хадгалах' : 'Үүсгэх'}</Button>
        </div>
      </form>
    </Dialog>
  )
}
