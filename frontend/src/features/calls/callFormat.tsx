import { PhoneIncoming, PhoneOutgoing } from 'lucide-react'
import { cn, fmtDuration } from '@/lib/utils'
import type { Call, CallDirection } from '@/lib/types'
import { useTicker } from '@/features/live/useTicker'
import { isLiveStatus } from './api'
import { DIRECTION_LABEL, callElapsedSec } from './callLabels'

export function DirectionIcon({ direction, className }: { direction: CallDirection; className?: string }) {
  const Icon = direction === 'inbound' ? PhoneIncoming : PhoneOutgoing
  return (
    <Icon aria-label={DIRECTION_LABEL[direction]}
      className={cn('h-4 w-4', direction === 'inbound' ? 'text-[var(--info-fg)]' : 'text-[var(--accent-2)]', className)} />
  )
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
