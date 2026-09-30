import { useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { AlertTriangle, FileText } from 'lucide-react'
import { Badge, Button, Card, CardHeader, Drawer, EmptyState, Skeleton, Table, TBody, TD, TH, THead, TR } from '@/components/ui'
import type { Invoice, InvoiceStatus, PaymentStatus } from '@/lib/types'
import { openInvoicePdf, useInvoice, useInvoices } from './hooks'
import { fmtDate, fmtMnt, fmtNum, fmtPeriod, invoiceStatusLabel, invoiceStatusTone } from './format'
import { errMsg } from './errors'
import { PayDialog } from './PayDialog'

export function InvoiceStatusBadge({ status }: { status: InvoiceStatus }) {
  return <Badge tone={invoiceStatusTone[status]} dot>{invoiceStatusLabel[status]}</Badge>
}

const paymentLabel: Record<PaymentStatus, string> = { pending: 'Хүлээгдэж буй', paid: 'Төлөгдсөн', failed: 'Амжилтгүй', expired: 'Хугацаа дууссан' }
const paymentTone = { pending: 'warning', paid: 'success', failed: 'danger', expired: 'neutral' } as const

async function viewPdf(inv: Invoice) {
  try { await openInvoicePdf(inv.id) } catch (e) { toast.error(errMsg(e)) }
}

function InvoiceDrawer({ id, onClose, onPay }: { id: string | null; onClose: () => void; onPay: (inv: Invoice) => void }) {
  const q = useInvoice(id)
  const inv = q.data?.invoice
  return (
    <Drawer open={!!id} onClose={onClose} width="max-w-xl" title={inv ? `Нэхэмжлэх ${inv.number}` : 'Нэхэмжлэх'}>
      {q.isLoading ? <div className="space-y-3 p-4"><Skeleton className="h-6" /><Skeleton className="h-32" /></div>
        : q.isError ? <EmptyState icon={<AlertTriangle />} title="Нэхэмжлэх ачаалж чадсангүй" description={errMsg(q.error)} />
        : inv && (
          <div className="space-y-5 p-4">
            <div className="grid grid-cols-2 gap-3 text-[13px]">
              <Field k="Төлөв" v={<InvoiceStatusBadge status={inv.status} />} />
              <Field k="Хугацаа" v={fmtPeriod(inv.periodStart, inv.periodEnd)} />
              <Field k="Төлөх огноо" v={fmtDate(inv.dueAt)} />
              <Field k="Төлсөн огноо" v={fmtDate(inv.paidAt)} />
            </div>
            <Table>
              <THead><tr><TH>Тайлбар</TH><TH className="text-right">Тоо</TH><TH className="text-right">Нэгж үнэ</TH><TH className="text-right">Дүн</TH></tr></THead>
              <TBody>
                {(inv.lines ?? []).map((l, i) => (
                  <TR key={i}>
                    <TD>{l.description}</TD>
                    <TD className="text-right tabular-nums">{fmtNum(l.quantity, 2)}</TD>
                    <TD className="text-right tabular-nums">{fmtMnt(l.unitMnt)}</TD>
                    <TD className="text-right tabular-nums">{fmtMnt(l.amountMnt)}</TD>
                  </TR>
                ))}
              </TBody>
            </Table>
            <div className="ml-auto w-full max-w-xs space-y-1 text-[13px]">
              <Total k="Дүн" v={fmtMnt(inv.subtotalMnt)} />
              <Total k="НӨАТ (10%)" v={fmtMnt(inv.vatMnt)} />
              <div className="border-t border-[var(--border)] pt-1"><Total k="Нийт" v={fmtMnt(inv.totalMnt)} strong /></div>
            </div>
            {(q.data?.payments ?? []).length > 0 && (
              <div>
                <div className="mb-2 text-xs font-medium text-[var(--fg-muted)]">Төлбөрүүд</div>
                <ul className="space-y-1.5 text-[13px]">
                  {q.data!.payments.map((p) => (
                    <li key={p.id} className="flex items-center justify-between gap-3 rounded-[var(--radius-sm)] border border-[var(--border-subtle)] px-3 py-2">
                      <span className="uppercase text-[var(--fg-muted)]">{p.provider}</span>
                      <span className="tabular-nums">{fmtMnt(p.amountMnt)}</span>
                      <span className="text-xs text-[var(--fg-muted)]">{fmtDate(p.paidAt ?? p.createdAt)}</span>
                      <Badge tone={paymentTone[p.status]}>{paymentLabel[p.status]}</Badge>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <div className="flex justify-end gap-2">
              <Button variant="secondary" onClick={() => viewPdf(inv)}><FileText />PDF харах</Button>
              {inv.status === 'open' && <Button onClick={() => onPay(inv)}>Төлөх</Button>}
            </div>
          </div>
        )}
    </Drawer>
  )
}

function Field({ k, v }: { k: string; v: ReactNode }) {
  return <div><div className="text-xs text-[var(--fg-muted)]">{k}</div><div className="mt-0.5 text-[var(--fg)]">{v}</div></div>
}
function Total({ k, v, strong }: { k: string; v: string; strong?: boolean }) {
  return (
    <div className="flex justify-between gap-4">
      <span className={strong ? 'font-medium text-[var(--fg)]' : 'text-[var(--fg-muted)]'}>{k}</span>
      <span className={strong ? 'font-mono font-semibold tabular-nums text-[var(--fg)]' : 'font-mono tabular-nums text-[var(--fg)]'}>{v}</span>
    </div>
  )
}

export function InvoicesTable() {
  const q = useInvoices()
  const [detailId, setDetailId] = useState<string | null>(null)
  const [payInvoice, setPayInvoice] = useState<Invoice | null>(null)
  return (
    <Card>
      <CardHeader title="Нэхэмжлэх" description="Сар бүрийн нэхэмжлэх (НӨАТ 10% орсон)" />
      {q.isLoading ? <div className="space-y-2 p-4"><Skeleton className="h-8" /><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
        : q.isError ? <EmptyState icon={<AlertTriangle />} title="Нэхэмжлэх ачаалж чадсангүй" description={errMsg(q.error)} />
        : (q.data ?? []).length === 0 ? <EmptyState icon={<FileText />} title="Нэхэмжлэх алга" description="Төлбөртэй багц сонгоход эсвэл тооцооны үе дуусахад нэхэмжлэх үүснэ." />
        : (
          <Table>
            <THead>
              <tr><TH>Дугаар</TH><TH>Хугацаа</TH><TH className="text-right">Нийт дүн</TH><TH>Төлөв</TH><TH>Төлөх огноо</TH><TH className="text-right">Үйлдэл</TH></tr>
            </THead>
            <TBody>
              {q.data!.map((inv) => (
                <TR key={inv.id} data-testid="invoice-row" className="cursor-pointer" onClick={() => setDetailId(inv.id)}>
                  <TD className="font-mono text-xs">{inv.number}</TD>
                  <TD className="whitespace-nowrap text-[var(--fg-muted)]">{fmtPeriod(inv.periodStart, inv.periodEnd)}</TD>
                  <TD className="text-right font-mono tabular-nums">{fmtMnt(inv.totalMnt)}</TD>
                  <TD><InvoiceStatusBadge status={inv.status} /></TD>
                  <TD className="whitespace-nowrap text-[var(--fg-muted)]">{fmtDate(inv.dueAt)}</TD>
                  <TD className="text-right" onClick={(e) => e.stopPropagation()}>
                    <div className="inline-flex gap-1.5">
                      {inv.status === 'open' && <Button size="sm" onClick={() => setPayInvoice(inv)}>Төлөх</Button>}
                      <Button size="sm" variant="secondary" onClick={() => viewPdf(inv)}>Харах</Button>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      <InvoiceDrawer id={detailId} onClose={() => setDetailId(null)} onPay={(inv) => { setDetailId(null); setPayInvoice(inv) }} />
      <PayDialog invoice={payInvoice} onClose={() => setPayInvoice(null)} />
    </Card>
  )
}
