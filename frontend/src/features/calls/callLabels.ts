import type { Call, CallDirection } from '@/lib/types'
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

/** Seconds elapsed for a call: live calls count from answer (or start); ended calls use `durationSec`. */
export function callElapsedSec(call: Pick<Call, 'status' | 'startedAt' | 'answeredAt' | 'durationSec'>, now: number): number {
  if (!isLiveStatus(call.status)) return call.durationSec
  const from = Date.parse(call.answeredAt || call.startedAt)
  return Number.isFinite(from) ? Math.max(0, (now - from) / 1000) : call.durationSec
}
