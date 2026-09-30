import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { QRCodeSVG } from 'qrcode.react'
import { toast } from 'sonner'
import { AlertTriangle, CheckCircle2, ExternalLink, Loader2, QrCode, RefreshCw } from 'lucide-react'
import { Button, Dialog } from '@/components/ui'
import { cn } from '@/lib/utils'
import type { Invoice, Payment } from '@/lib/types'
import { billingKeys, useBillingLiveSync, usePay, usePayment, type PaymentProvider } from './hooks'
import { fmtCountdown, fmtMnt, fmtPeriod, qrImageSrc } from './format'
import { errMsg } from './errors'

interface ProviderOption { id: PaymentProvider; label: string; hint: string }
function providerOptions(): ProviderOption[] {
  const opts: ProviderOption[] = [{ id: 'qpay', label: 'QPay', hint: 'Банкны апп-аар QR уншуулж төлнө' }]
  if (import.meta.env.DEV) opts.push({ id: 'mock', label: 'Mock (хөгжүүлэлт)', hint: 'Туршилтын төлбөр — зөвхөн DEV орчинд' })
  return opts
}

/** Re-renders every second while `active`; returns the current epoch ms. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [active])
  return now
}

export interface PayDialogProps {
  /** Invoice to pay; `null` closes the dialog. */
  invoice: Invoice | null
  onClose: () => void
  onPaid?: (payment: Payment) => void
}

export function PayDialog({ invoice, onClose, onPaid }: PayDialogProps) {
  return (
    <Dialog open={!!invoice} onClose={onClose} className="max-w-md"
      title="Төлбөр төлөх"
      description={invoice ? `Нэхэмжлэх ${invoice.number} · ${fmtPeriod(invoice.periodStart, invoice.periodEnd)}` : undefined}>
      {invoice && <PayFlow key={invoice.id} invoice={invoice} onClose={onClose} onPaid={onPaid} />}
    </Dialog>
  )
}

function PayFlow({ invoice, onClose, onPaid }: { invoice: Invoice; onClose: () => void; onPaid?: (p: Payment) => void }) {
  const qc = useQueryClient()
  const options = providerOptions()
  const [provider, setProvider] = useState<PaymentProvider>('qpay')
  const [paymentId, setPaymentId] = useState<string | null>(null)
  const pay = usePay()
  const paymentQ = usePayment(paymentId)
  const payment = paymentQ.data
  useBillingLiveSync()

  const pending = payment?.status === 'pending'
  const now = useNow(!!pending && !!payment?.expiresAt)
  const expiresAt = payment?.expiresAt ? new Date(payment.expiresAt).getTime() : null
  const remaining = expiresAt !== null ? expiresAt - now : null
  const expired = payment?.status === 'expired' || payment?.status === 'failed' || (pending && remaining !== null && remaining <= 0)

  const notified = useRef(false)
  useEffect(() => {
    if (payment?.status !== 'paid' || notified.current) return
    notified.current = true
    toast.success('Төлбөр амжилттай төлөгдлөө')
    void qc.invalidateQueries({ queryKey: billingKeys.subscription })
    void qc.invalidateQueries({ queryKey: billingKeys.invoices })
    void qc.invalidateQueries({ queryKey: billingKeys.invoice(invoice.id) })
    onPaid?.(payment)
  }, [payment, qc, invoice.id, onPaid])

  const start = async () => {
    try {
      const p = await pay.mutateAsync({ invoiceId: invoice.id, provider })
      setPaymentId(p.id)
    } catch (e) {
      toast.error(errMsg(e))
    }
  }

  if (!paymentId || !payment) {
    return (
      <div className="space-y-4">
        <Amount value={invoice.totalMnt} />
        <fieldset>
          <legend className="mb-2 text-xs font-medium text-[var(--fg-muted)]">Төлбөрийн хэрэгсэл</legend>
          <div className="space-y-2" role="radiogroup" aria-label="Төлбөрийн хэрэгсэл">
            {options.map((o) => (
              <label key={o.id}
                className={cn('flex cursor-pointer items-center gap-3 rounded-[var(--radius)] border px-3 py-2.5 transition-colors',
                  provider === o.id ? 'border-[var(--accent)] bg-[var(--surface-2)]' : 'border-[var(--border)] hover:border-[var(--border-strong)]')}>
                <input type="radio" name="provider" value={o.id} checked={provider === o.id} onChange={() => setProvider(o.id)}
                  className="accent-[var(--accent)]" aria-label={o.label} />
                <QrCode className="h-4 w-4 text-[var(--fg-muted)]" aria-hidden />
                <span className="min-w-0">
                  <span className="block text-[13px] font-medium text-[var(--fg)]">{o.label}</span>
                  <span className="block text-xs text-[var(--fg-muted)]">{o.hint}</span>
                </span>
              </label>
            ))}
          </div>
        </fieldset>
        <div className="flex justify-end gap-2 pt-1">
          <Button variant="ghost" onClick={onClose}>Болих</Button>
          <Button onClick={start} loading={pay.isPending || (!!paymentId && paymentQ.isLoading)}>QR код үүсгэх</Button>
        </div>
      </div>
    )
  }

  if (payment.status === 'paid') {
    return (
      <div className="flex flex-col items-center gap-3 py-6 text-center" role="status">
        <CheckCircle2 className="h-10 w-10 text-[var(--success)]" aria-hidden />
        <div className="text-sm font-semibold text-[var(--fg)]">Төлбөр амжилттай төлөгдлөө</div>
        <div className="text-xs text-[var(--fg-muted)]">{fmtMnt(payment.amountMnt)} · Нэхэмжлэх {invoice.number}</div>
        <Button className="mt-2" onClick={onClose}>Хаах</Button>
      </div>
    )
  }

  if (expired) {
    return (
      <div className="flex flex-col items-center gap-3 py-6 text-center" role="alert">
        <AlertTriangle className="h-9 w-9 text-[var(--warning)]" aria-hidden />
        <div className="text-sm font-semibold text-[var(--fg)]">
          {payment.status === 'failed' ? 'Төлбөр амжилтгүй боллоо' : 'QR кодын хугацаа дууссан'}
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" onClick={onClose}>Хаах</Button>
          <Button onClick={() => { setPaymentId(null); void start() }} loading={pay.isPending}><RefreshCw />Дахин үүсгэх</Button>
        </div>
      </div>
    )
  }

  const links = payment.deepLinks ?? []
  return (
    <div className="space-y-4">
      <Amount value={payment.amountMnt || invoice.totalMnt} />
      <div className="flex flex-col items-center gap-2">
        <div className="rounded-[var(--radius)] border border-[var(--border)] bg-white p-3" data-testid="payment-qr">
          {payment.qrImage
            ? <img src={qrImageSrc(payment.qrImage)} alt="Төлбөрийн QR код" className="h-48 w-48" />
            : payment.qrText
              ? <QRCodeSVG value={payment.qrText} size={192} level="M" role="img" aria-label="Төлбөрийн QR код" />
              : <div className="flex h-48 w-48 items-center justify-center text-xs text-zinc-500">QR код алга</div>}
        </div>
        <div className="flex items-center gap-2 text-xs text-[var(--fg-muted)]">
          <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />
          <span>Төлбөрийг шалгаж байна…</span>
          {remaining !== null && (
            <span className="tabular-nums" data-testid="payment-countdown">· Хүчинтэй: <span className="font-medium text-[var(--fg)]">{fmtCountdown(remaining)}</span></span>
          )}
        </div>
      </div>
      {links.length > 0 && (
        <div>
          <div className="mb-2 text-xs font-medium text-[var(--fg-muted)]">Банкны апп-аар төлөх</div>
          <ul className="grid max-h-56 grid-cols-2 gap-1.5 overflow-y-auto sm:grid-cols-3" aria-label="Банкны апп-ууд">
            {links.map((l) => (
              <li key={`${l.name}-${l.link}`}>
                <a href={l.link} target="_blank" rel="noopener noreferrer"
                  className="flex h-10 items-center gap-2 rounded-[var(--radius-sm)] border border-[var(--border)] px-2 text-xs text-[var(--fg)] transition-colors hover:border-[var(--border-strong)] hover:bg-[var(--surface-2)]">
                  {l.logo ? <img src={l.logo} alt="" className="h-5 w-5 shrink-0 rounded" loading="lazy" /> : <ExternalLink className="h-4 w-4 shrink-0 text-[var(--fg-subtle)]" aria-hidden />}
                  <span className="truncate">{l.name}</span>
                </a>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex justify-end">
        <Button variant="ghost" onClick={onClose}>Дараа төлөх</Button>
      </div>
    </div>
  )
}

function Amount({ value }: { value: number }) {
  return (
    <div className="rounded-[var(--radius)] border border-[var(--border)] bg-[var(--surface-inset)] px-4 py-3 text-center">
      <div className="text-[11px] font-medium uppercase tracking-[0.04em] text-[var(--fg-muted)]">Төлөх дүн</div>
      <div className="mt-1 font-mono text-2xl font-medium tabular-nums text-[var(--fg)]" data-testid="pay-amount">{fmtMnt(value)}</div>
    </div>
  )
}
