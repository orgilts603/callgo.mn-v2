import type { Call, LiveEvent, TranscriptTurn } from '@/lib/types'

/** Resolve which call an event belongs to (envelope first, then payload). */
export function eventCallId(ev: LiveEvent): string | null {
  if (ev.callId) return ev.callId
  const p = ev.payload as { call?: Call; turn?: TranscriptTurn } | null | undefined
  return p?.call?.id ?? p?.turn?.callId ?? null
}
