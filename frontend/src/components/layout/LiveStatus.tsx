import { Link } from 'react-router-dom'
import { PhoneCall } from 'lucide-react'
import { useLive } from '@/lib/ws'
import { cn } from '@/lib/utils'

type WsStatus = 'connecting' | 'open' | 'closed'
const wsMeta: Record<WsStatus, { label: string; dot: string; text: string; title: string }> = {
  open: { label: 'Онлайн', dot: 'bg-[var(--success)]', text: 'text-[var(--fg-muted)]', title: 'Шууд мэдээллийн холболт идэвхтэй' },
  connecting: { label: 'Холбогдож байна', dot: 'bg-[var(--warning)] animate-pulse', text: 'text-[var(--warning-fg)]', title: 'Шууд мэдээллийн сувагт холбогдож байна' },
  closed: { label: 'Салсан', dot: 'bg-[var(--danger)]', text: 'text-[var(--danger-fg)]', title: 'Шууд мэдээллийн холболт тасарсан. Автоматаар дахин холбогдоно.' },
}

/** Pill showing the live WebSocket status (useLive().status). */
export function LiveStatusPill({ className }: { className?: string }) {
  const status = useLive((s) => s.status)
  const m = wsMeta[status]
  return (
    <span
      role="status"
      aria-label={`Холболт: ${m.label}`}
      data-status={status}
      title={m.title}
      className={cn('inline-flex h-7 items-center gap-2 rounded-full border border-[var(--border)] bg-[var(--surface-1)] px-2.5 text-xs font-medium', m.text, className)}
    >
      <span className={cn('h-1.5 w-1.5 rounded-full', m.dot)} aria-hidden />
      {m.label}
    </span>
  )
}

/** Active calls counter from the live store; pulses while calls are in progress. */
export function ActiveCallsBadge({ className }: { className?: string }) {
  const count = useLive((s) => Object.keys(s.activeCalls).length)
  const live = count > 0
  return (
    <Link
      to="/live"
      aria-label={`Идэвхтэй дуудлага: ${count}`}
      title="Live Desk нээх"
      data-active={live || undefined}
      className={cn(
        'inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium transition-colors',
        live
          ? 'border-[var(--success-border)] bg-[var(--success-soft)] text-[var(--success-fg)] hover:brightness-110'
          : 'border-[var(--border)] bg-[var(--surface-1)] text-[var(--fg-muted)] hover:text-[var(--fg)]',
        className,
      )}
    >
      <span className="relative flex h-3.5 w-3.5 items-center justify-center" aria-hidden>
        {live && <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-[var(--success)] opacity-30" />}
        <PhoneCall className="relative h-3.5 w-3.5" />
      </span>
      <span className="tabular">{count}</span>
      <span className="hidden xl:inline">идэвхтэй</span>
    </Link>
  )
}
