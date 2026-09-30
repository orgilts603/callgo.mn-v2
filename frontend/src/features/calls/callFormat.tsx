import { PhoneIncoming, PhoneOutgoing } from 'lucide-react'
import { cn, fmtDuration } from '@/lib/utils'
import type { Call, CallDirection } from '@/lib/types'
import { useTicker } from '@/features/live/useTicker'
import { isLiveStatus } from './api'

export const DIRECTION_LABEL: Record<CallDirection, string> = { inbound: 'Ирсэн', outbound: 'Гарсан' }

export const END_REASON_LABEL: Record<string, string> = {
  hangup_customer: 'Харилцагч таслав',
  hangup_agent: 'Агент таслав',
  no_answer: 'Хариулаагүй',
  busy: 'Завгүй',
  failed: 'Амжилтгүй',
  max_duration: 'Хугацаа хэтэрсэн',
  transferred: 'Шилжүүлсэн',
  voicemail: 'Дуут шуудан',
}

export function DirectionIcon({ direction, className }: { direction: CallDirection; className?: string }) {
  const Icon = direction === 'inbound' ? PhoneIncoming : PhoneOutgoing
  return (
    <Icon aria-label={DIRECTION_LABEL[direction]}
      className={cn('h-4 w-4', direction === 'inbound' ? 'text-[var(--info-fg)]' : 'text-[var(--accent-2)]', className)} />
  )
}

/** Seconds elapsed for a call: live calls count from answer (or start); ended calls use `durationSec`. */
export function callElapsedSec(call: Pick<Call, 'status' | 'startedAt' | 'answeredAt' | 'durationSec'>, now: number): number {
  if (!isLiveStatus(call.status)) return call.durationSec
  const from = Date.parse(call.answeredAt || call.startedAt)
  return Number.isFinite(from) ? Math.max(0, (now - from) / 1000) : call.durationSec
}

function TickingDuration({ call }: { call: Call }) {
  const now = useTicker(1000)
  return <>{fmtDuration(callElapsedSec(call, now))}</>
}

/** Call duration that ticks every second while the call is live. */
export function CallDuration({ call, className }: { call: Call; className?: string }) {
  return (
    <span className={cn('tabular-nums', className)}>
      {isLiveStatus(call.status) ? <TickingDuration call={call} /> : fmtDuration(call.durationSec)}
    </span>
  )
}
