import { memo } from 'react'
import { format } from 'date-fns'
import {
  Activity, BookA, Bot, ChevronsLeft, ChevronsRight, Megaphone, MessageSquareText, PhoneCall, PhoneIncoming, PhoneOff, RefreshCw, Wifi,
} from 'lucide-react'
import { AgentStateBadge, Card } from '@/components/ui'
import { cn, fmtPhone } from '@/lib/utils'
import type {
  AgentStatePayload, CallEventPayload, CampaignProgressPayload, EventType, LexiconUpdatedPayload, LiveEvent, SystemPayload,
  TranscriptFinalPayload,
} from '@/lib/types'
import { END_REASON_LABEL } from '@/features/calls/callFormat'
import { eventCallId } from '@/features/calls/eventUtils'

const ICONS: Partial<Record<EventType, typeof Activity>> = {
  'call.started': PhoneIncoming, 'call.ringing': PhoneCall, 'call.answered': PhoneCall, 'call.ended': PhoneOff,
  'call.updated': RefreshCw, 'transcript.final': MessageSquareText, 'agent.state': Bot, 'campaign.progress': Megaphone,
  'lexicon.updated': BookA, system: Wifi,
}
const TONES: Partial<Record<EventType, string>> = {
  'call.started': 'text-[var(--info)]', 'call.ringing': 'text-[var(--warning)]', 'call.answered': 'text-[var(--success)]', 'call.ended': 'text-[var(--danger)]',
  'lexicon.updated': 'text-[var(--accent-fg)]',
}
const SPEAKER: Record<string, string> = { customer: 'Харилцагч', agent: 'Агент', human: 'Оператор' }

function describe(ev: LiveEvent) {
  switch (ev.type) {
    case 'call.started': case 'call.ringing': case 'call.answered': case 'call.updated': case 'call.ended': {
      const p = ev.payload as Partial<CallEventPayload>
      const label = { 'call.started': 'Дуудлага эхэллээ', 'call.ringing': 'Дуугарч байна', 'call.answered': 'Хариуллаа', 'call.updated': 'Шинэчлэгдлээ', 'call.ended': 'Дууслаа' }[ev.type]
      const who = p.call ? fmtPhone(p.call.direction === 'inbound' ? p.call.fromNumber : p.call.toNumber) : ''
      const reason = ev.type === 'call.ended' && p.endReason ? ` · ${END_REASON_LABEL[p.endReason] ?? p.endReason}` : ''
      return <><span className="font-medium text-[var(--fg)]">{label}</span>{who && <span className="text-[var(--fg-muted)]"> {who}</span>}{reason}</>
    }
    case 'transcript.final': {
      const { turn } = ev.payload as TranscriptFinalPayload
      return <><span className="font-medium text-[var(--fg)]">{SPEAKER[turn.speaker] ?? turn.speaker}:</span> {turn.text}</>
    }
    case 'agent.state':
      return <AgentStateBadge state={(ev.payload as AgentStatePayload).state} />
    case 'campaign.progress': {
      const { campaign } = ev.payload as CampaignProgressPayload
      return <><span className="font-medium text-[var(--fg)]">{campaign.name}</span> {campaign.completed + campaign.failed}/{campaign.total}</>
    }
    case 'lexicon.updated': {
      const { correction } = ev.payload as LexiconUpdatedPayload
      return <>Lexicon: <span className="line-through">{correction.wrong}</span> → <span className="text-[var(--fg)]">{correction.correct}</span></>
    }
    case 'system': {
      const p = ev.payload as SystemPayload
      return p.hello ? `Холбогдлоо · ${p.activeCalls?.length ?? 0} идэвхтэй` : (p.message ?? 'Систем')
    }
    default:
      return ev.type
  }
}

function safeTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : format(d, 'HH:mm:ss')
}

const FeedItem = memo(function FeedItem({ ev, onOpenCall }: { ev: LiveEvent; onOpenCall: (id: string) => void }) {
  const Icon = ICONS[ev.type] ?? Activity
  const callId = eventCallId(ev)
  const body = (
    <>
      <Icon className={cn('mt-0.5 h-3.5 w-3.5 shrink-0', TONES[ev.type] ?? 'text-[var(--fg-subtle)]')} />
      <span className="min-w-0 flex-1 truncate text-[var(--fg-muted)]">{describe(ev)}</span>
      <span className="shrink-0 tabular-nums text-[var(--fg-subtle)]">{safeTime(ev.at)}</span>
    </>
  )
  const cls = 'flex w-full items-start gap-2 px-3 py-1.5 text-left text-[11px] leading-4'
  return callId
    ? <li><button type="button" className={cn(cls, 'hover:bg-[var(--surface-2)]')} onClick={() => onOpenCall(callId)}>{body}</button></li>
    : <li className={cls}>{body}</li>
})

export interface EventFeedProps {
  events: LiveEvent[]
  collapsed: boolean
  onToggle: () => void
  onOpenCall: (id: string) => void
}

/** Compact, collapsible list of the latest live events. */
export function EventFeed({ events, collapsed, onToggle, onOpenCall }: EventFeedProps) {
  if (collapsed) {
    return (
      <Card className="flex w-10 shrink-0 flex-col items-center py-2">
        <button type="button" onClick={onToggle} aria-label="Үйл явдлууд нээх" title="Сүүлийн үйл явдлууд"
          className="rounded p-1 text-[var(--fg-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)]">
          <ChevronsLeft className="h-4 w-4" />
        </button>
        <span className="mt-3 text-[11px] text-[var(--fg-subtle)] [writing-mode:vertical-rl]">Сүүлийн үйл явдлууд</span>
      </Card>
    )
  }
  return (
    <Card className="flex w-80 shrink-0 flex-col overflow-hidden" data-testid="event-feed">
      <div className="flex items-center justify-between border-b border-[var(--border)] px-3 py-2.5">
        <h3 className="text-xs font-semibold text-[var(--fg)]">Сүүлийн үйл явдлууд</h3>
        <button type="button" onClick={onToggle} aria-label="Үйл явдлууд хураах"
          className="rounded p-1 text-[var(--fg-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)]">
          <ChevronsRight className="h-4 w-4" />
        </button>
      </div>
      {events.length === 0
        ? <p className="px-3 py-6 text-center text-xs text-[var(--fg-subtle)]">Үйл явдал хүлээж байна…</p>
        : <ul className="max-h-[calc(100vh-18rem)] divide-y divide-[var(--border-subtle)] overflow-y-auto">{events.map((ev, i) => <FeedItem key={ev.id || `i${i}`} ev={ev} onOpenCall={onOpenCall} />)}</ul>}
    </Card>
  )
}
