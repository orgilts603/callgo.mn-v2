import { useState } from 'react'
import { toast } from 'sonner'
import { ChevronLeft, ChevronRight, RotateCw } from 'lucide-react'
import { Badge, Button, Drawer, EmptyState, Skeleton, Table, TBody, TD, TH, THead, TR, type BadgeTone } from '@/components/ui'
import type { Webhook, WebhookDelivery } from '@/lib/types'
import { fmtDateTime } from '@/lib/utils'
import { ErrorNote, errMsg } from './common'
import { DELIVERY_PAGE_SIZE, useRetryDelivery, useWebhookDeliveries } from './hooks'

const tone: Record<WebhookDelivery['status'], BadgeTone> = { pending: 'warning', delivered: 'success', failed: 'danger' }
const label: Record<WebhookDelivery['status'], string> = { pending: 'Хүлээгдэж байна', delivered: 'Хүргэгдсэн', failed: 'Амжилтгүй' }

export function DeliveryStatusBadge({ status }: { status: WebhookDelivery['status'] }) {
  return <Badge tone={tone[status]} dot>{label[status]}</Badge>
}

/** Result of a test delivery. */
export function DeliveryResult({ delivery }: { delivery: WebhookDelivery }) {
  return (
    <dl className="grid grid-cols-[8rem_1fr] gap-x-3 gap-y-2 text-sm" data-testid="delivery-result">
      <dt className="text-[var(--fg-muted)]">Төлөв</dt><dd><DeliveryStatusBadge status={delivery.status} /></dd>
      <dt className="text-[var(--fg-muted)]">HTTP код</dt><dd className="font-mono tabular-nums">{delivery.responseCode || '—'}</dd>
      <dt className="text-[var(--fg-muted)]">Оролдлого</dt><dd className="tabular-nums">{delivery.attempts}</dd>
      <dt className="text-[var(--fg-muted)]">Алдаа</dt><dd className="break-all text-[var(--danger)]">{delivery.lastError || <span className="text-[var(--fg-subtle)]">—</span>}</dd>
    </dl>
  )
}

export function WebhookDeliveriesDrawer({ webhook, onClose }: { webhook: Webhook | null; onClose: () => void }) {
  const [offset, setOffset] = useState(0)
  const q = useWebhookDeliveries(webhook?.id ?? null, offset)
  const retry = useRetryDelivery()
  const items = q.data?.items ?? []
  const total = q.data?.total ?? 0

  return (
    <Drawer open={webhook !== null} onClose={() => { setOffset(0); onClose() }} title={webhook ? `Хүргэлтүүд: ${webhook.url}` : ''} width="max-w-3xl">
      {webhook && (
        <div className="p-4">
          {q.isLoading ? (
            <div className="space-y-2"><Skeleton className="h-9" /><Skeleton className="h-9" /></div>
          ) : q.isError ? (
            <ErrorNote error={q.error} />
          ) : items.length === 0 ? (
            <EmptyState title="Хүргэлт алга" description="Үйл явдал болмогц энд харагдана. «Тест» товчоор шалгаж болно." />
          ) : (
            <>
              <Table>
                <THead><TR><TH>Үйл явдал</TH><TH>Төлөв</TH><TH className="text-right">Оролдлого</TH><TH>HTTP</TH><TH>Алдаа</TH><TH>Огноо</TH><TH className="text-right">Үйлдэл</TH></TR></THead>
                <TBody>
                  {items.map((d) => (
                    <TR key={d.id}>
                      <TD className="font-mono text-xs">{d.eventType}</TD>
                      <TD><DeliveryStatusBadge status={d.status} /></TD>
                      <TD className="text-right tabular-nums">{d.attempts}</TD>
                      <TD className="font-mono tabular-nums">{d.responseCode || '—'}</TD>
                      <TD className="max-w-[14rem] truncate text-xs text-[var(--danger)]" title={d.lastError}>{d.lastError || '—'}</TD>
                      <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtDateTime(d.createdAt)}</TD>
                      <TD className="text-right">
                        {d.status !== 'delivered' && (
                          <Button size="sm" variant="outline" aria-label={`Дахин илгээх ${d.eventType}`}
                            loading={retry.isPending && retry.variables === d.id}
                            onClick={() => retry.mutate(d.id, { onSuccess: () => toast.success('Дахин илгээлээ'), onError: (e) => toast.error(errMsg(e)) })}>
                            <RotateCw className="h-3.5 w-3.5" />Дахин
                          </Button>
                        )}
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
              <div className="mt-3 flex items-center justify-between text-xs text-[var(--fg-muted)]">
                <span className="tabular-nums">{offset + 1}–{offset + items.length} / {total}</span>
                <div className="flex gap-1">
                  <Button size="icon" variant="ghost" aria-label="Өмнөх" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - DELIVERY_PAGE_SIZE))}><ChevronLeft className="h-4 w-4" /></Button>
                  <Button size="icon" variant="ghost" aria-label="Дараах" disabled={offset + items.length >= total} onClick={() => setOffset(offset + DELIVERY_PAGE_SIZE)}><ChevronRight className="h-4 w-4" /></Button>
                </div>
              </div>
            </>
          )}
        </div>
      )}
    </Drawer>
  )
}
