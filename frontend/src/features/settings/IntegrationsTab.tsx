import { useState } from 'react'
import { toast } from 'sonner'
import { History, KeyRound, Pencil, Plus, Trash2, Webhook as WebhookIcon, Zap } from 'lucide-react'
import {
  Badge, Button, Card, CardBody, Dialog, EmptyState, Skeleton, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import type { Webhook, WebhookDelivery } from '@/lib/types'
import { fmtAgo } from '@/lib/utils'
import { ConfirmDialog, ErrorNote, Switch, errMsg } from './common'
import { FeatureGate } from './FeatureGate'
import { useDeleteWebhook, useRotateWebhookSecret, useTestWebhook, useUpdateWebhook, useWebhooks } from './hooks'
import { SMSSection } from './SMSSection'
import { RotatedSecretDialog, WebhookDialog } from './WebhookDialog'
import { DeliveryResult, WebhookDeliveriesDrawer } from './WebhookDeliveriesDrawer'

function EventChips({ events }: { events: string[] }) {
  if (events.includes('*')) return <Badge tone="accent" className="font-mono">* бүгд</Badge>
  const shown = events.slice(0, 3)
  return (
    <div className="flex max-w-xs flex-wrap gap-1">
      {shown.map((e) => <Badge key={e} tone="neutral" className="font-mono">{e}</Badge>)}
      {events.length > shown.length && <Badge tone="neutral" title={events.join(', ')}>+{events.length - shown.length}</Badge>}
    </div>
  )
}

function LastStatus({ w }: { w: Webhook }) {
  if (!w.lastAt) return <span className="text-[var(--fg-subtle)]">—</span>
  const ok = w.lastStatus >= 200 && w.lastStatus < 300
  return (
    <div className="flex items-center gap-2">
      <Badge tone={ok ? 'success' : 'danger'} className="font-mono">{w.lastStatus || 'ERR'}</Badge>
      <span className="text-xs text-[var(--fg-muted)]">{fmtAgo(w.lastAt)}</span>
    </div>
  )
}

function WebhooksSection() {
  const hooks = useWebhooks()
  const update = useUpdateWebhook()
  const del = useDeleteWebhook()
  const rotate = useRotateWebhookSecret()
  const test = useTestWebhook()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Webhook | null>(null)
  const [deleting, setDeleting] = useState<Webhook | null>(null)
  const [rotating, setRotating] = useState<Webhook | null>(null)
  const [secret, setSecret] = useState<string | null>(null)
  const [deliveriesOf, setDeliveriesOf] = useState<Webhook | null>(null)
  const [testResult, setTestResult] = useState<{ webhook: Webhook; delivery: WebhookDelivery } | null>(null)
  const items = hooks.data ?? []

  return (
    <section aria-labelledby="webhooks-heading" className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="webhooks-heading" className="text-sm font-semibold text-[var(--fg)]">Webhook</h2>
          <p className="mt-1 max-w-3xl text-sm text-[var(--fg-muted)]">
            Үйл явдал бүрийг гарын үсэгтэй (<span className="font-mono text-xs">X-CallGo-Signature</span>) JSON-оор таны серверт илгээнэ. 20 дараалсан алдааны дараа идэвхгүй болно.
          </p>
        </div>
        {!hooks.isError && <Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Webhook нэмэх</Button>}
      </div>
      <FeatureGate error={hooks.error} feature="Webhook">
        <Card>
          {hooks.isLoading ? (
            <CardBody className="space-y-2"><Skeleton className="h-9" /><Skeleton className="h-9" /></CardBody>
          ) : hooks.isError ? (
            <CardBody><ErrorNote error={hooks.error} /></CardBody>
          ) : items.length === 0 ? (
            <EmptyState icon={<WebhookIcon className="h-8 w-8" />} title="Webhook бүртгэгдээгүй байна" description="Дуудлага дуусах зэрэг үйл явдлыг CRM эсвэл өөрийн систем рүү илгээнэ."
              action={<Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Webhook нэмэх</Button>} />
          ) : (
            <Table>
              <THead><TR><TH>URL</TH><TH>Үйл явдал</TH><TH>Идэвхтэй</TH><TH className="text-right">Алдаа</TH><TH>Сүүлийн хүргэлт</TH><TH className="text-right">Үйлдэл</TH></TR></THead>
              <TBody>
                {items.map((w) => (
                  <TR key={w.id}>
                    <TD>
                      <div className="max-w-xs truncate font-mono text-xs" title={w.url}>{w.url}</div>
                      {w.description && <div className="text-xs text-[var(--fg-subtle)]">{w.description}</div>}
                    </TD>
                    <TD><EventChips events={w.events} /></TD>
                    <TD>
                      <Switch checked={w.active} label={`Идэвхтэй ${w.url}`} disabled={update.isPending && update.variables?.id === w.id}
                        onChange={(active) => update.mutate({ id: w.id, body: { active } }, { onError: (e) => toast.error(errMsg(e)) })} />
                    </TD>
                    <TD className="text-right tabular-nums">{w.failureCount > 0 ? <span className="text-[var(--danger)]">{w.failureCount}</span> : 0}</TD>
                    <TD><LastStatus w={w} /></TD>
                    <TD className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="outline" aria-label={`Тест ${w.url}`} loading={test.isPending && test.variables === w.id}
                          onClick={() => test.mutate(w.id, { onSuccess: (r) => setTestResult({ webhook: w, delivery: r.delivery }), onError: (e) => toast.error(errMsg(e)) })}>
                          <Zap className="h-3.5 w-3.5" />Тест
                        </Button>
                        <Button size="icon" variant="ghost" aria-label={`Хүргэлтүүд ${w.url}`} title="Хүргэлтүүд" onClick={() => setDeliveriesOf(w)}><History className="h-4 w-4" /></Button>
                        <Button size="icon" variant="ghost" aria-label={`Secret шинэчлэх ${w.url}`} title="Secret шинэчлэх" onClick={() => setRotating(w)}><KeyRound className="h-4 w-4" /></Button>
                        <Button size="icon" variant="ghost" aria-label={`Засах ${w.url}`} onClick={() => setEditing(w)}><Pencil className="h-4 w-4" /></Button>
                        <Button size="icon" variant="ghost" aria-label={`Устгах ${w.url}`} onClick={() => setDeleting(w)}><Trash2 className="h-4 w-4 text-red-400" /></Button>
                      </div>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </Card>
      </FeatureGate>

      <WebhookDialog open={creating || editing !== null} initial={editing} onClose={() => { setCreating(false); setEditing(null) }} />
      <ConfirmDialog open={deleting !== null} title="Webhook устгах уу?" description={deleting?.url} loading={del.isPending} onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting.id, { onSuccess: () => { toast.success('Устгагдлаа'); setDeleting(null) }, onError: (e) => toast.error(errMsg(e)) })} />
      <ConfirmDialog open={rotating !== null} title="Secret шинэчлэх үү?" description="Хуучин secret тэр даруй хүчингүй болно." confirmLabel="Шинэчлэх" loading={rotate.isPending} onClose={() => setRotating(null)}
        onConfirm={() => rotating && rotate.mutate(rotating.id, { onSuccess: (r) => { setRotating(null); setSecret(r.secret) }, onError: (e) => toast.error(errMsg(e)) })} />
      <RotatedSecretDialog secret={secret} onClose={() => setSecret(null)} />
      <Dialog open={testResult !== null} onClose={() => setTestResult(null)} title="Тестийн үр дүн" description={testResult?.webhook.url}
        footer={<Button onClick={() => setTestResult(null)}>Хаах</Button>}>
        {testResult && <DeliveryResult delivery={testResult.delivery} />}
      </Dialog>
      <WebhookDeliveriesDrawer webhook={deliveriesOf} onClose={() => setDeliveriesOf(null)} />
    </section>
  )
}

export default function IntegrationsTab() {
  return (
    <div className="space-y-10">
      <WebhooksSection />
      <SMSSection />
    </div>
  )
}
