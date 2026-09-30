import { Link } from 'react-router-dom'
import { ArrowUpRight, FlaskConical, History, Megaphone, Radio } from 'lucide-react'
import { Card, CardBody, CardHeader } from '@/components/ui'
import { LiveStatusPill } from '@/components/layout/LiveStatus'
import { useLive } from '@/lib/ws'
import { fmtAgoMn } from './format'

const links = [
  { to: '/live', label: 'Live Desk', desc: 'Идэвхтэй дуудлагыг шууд хянах', icon: Radio },
  { to: '/campaigns?new=1', label: 'Шинэ кампанит ажил', desc: 'Гарах дуудлагын жагсаалт эхлүүлэх', icon: Megaphone },
  { to: '/calls', label: 'Дуудлагын түүх', desc: 'Транскрипт, бичлэг, дүгнэлт', icon: History },
]

export function SystemCard() {
  const status = useLive((s) => s.status)
  const active = useLive((s) => Object.keys(s.activeCalls).length)
  const lastEvent = useLive((s) => s.lastEvent)

  return (
    <Card className="flex flex-col">
      <CardHeader title="Систем" description="Холболт ба түргэн холбоос" actions={<LiveStatusPill />} />
      <CardBody className="flex flex-1 flex-col gap-4">
        <dl className="grid grid-cols-2 gap-3 text-xs">
          <div className="rounded-[var(--radius)] border border-[var(--border-subtle)] bg-[var(--surface-inset)] px-3 py-2">
            <dt className="text-[var(--fg-subtle)]">Идэвхтэй дуудлага</dt>
            <dd className="tabular mt-0.5 text-base font-semibold text-[var(--fg)]">{active}</dd>
          </div>
          <div className="rounded-[var(--radius)] border border-[var(--border-subtle)] bg-[var(--surface-inset)] px-3 py-2">
            <dt className="text-[var(--fg-subtle)]">Сүүлийн үйл явдал</dt>
            <dd className="mt-0.5 truncate text-[13px] font-medium text-[var(--fg)]" title={lastEvent?.type}>
              {lastEvent ? fmtAgoMn(lastEvent.at) : status === 'open' ? 'Хүлээж байна' : '—'}
            </dd>
          </div>
        </dl>

        <div className="flex gap-2.5 rounded-[var(--radius)] border border-[var(--accent-border)] bg-[var(--accent-soft)] px-3 py-2.5 text-xs leading-relaxed text-[var(--fg-muted)]">
          <FlaskConical className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[var(--accent-fg)]" />
          <p>
            Туршилтын орчинд backend нь <span className="font-medium text-[var(--fg)]">mock телефони</span> болон{' '}
            <span className="font-medium text-[var(--fg)]">дуудлагын симулятор</span> ашиглан жишээ дуудлага, транскрипт үүсгэдэг.
            Бодит цагийн урсгалыг Live Desk дээр харна уу.
          </p>
        </div>

        <nav aria-label="Түргэн холбоос" className="-mx-1 mt-auto space-y-0.5">
          {links.map(({ to, label, desc, icon: Icon }) => (
            <Link key={to} to={to} className="group flex items-center gap-3 rounded-[var(--radius-sm)] px-1 py-1.5 transition-colors hover:bg-[var(--surface-2)]">
              <span className="flex h-7 w-7 items-center justify-center rounded-md border border-[var(--border)] bg-[var(--surface-2)] text-[var(--fg-muted)] group-hover:text-[var(--accent-fg)]">
                <Icon className="h-3.5 w-3.5" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block text-[13px] font-medium text-[var(--fg)]">{label}</span>
                <span className="block truncate text-[11px] text-[var(--fg-subtle)]">{desc}</span>
              </span>
              <ArrowUpRight className="h-3.5 w-3.5 text-[var(--fg-subtle)] opacity-0 transition-opacity group-hover:opacity-100" />
            </Link>
          ))}
        </nav>
      </CardBody>
    </Card>
  )
}
