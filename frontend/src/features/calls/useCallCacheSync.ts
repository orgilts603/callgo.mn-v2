import { useEffect } from 'react'
import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useLive } from '@/lib/ws'
import type { Call, CallEventPayload, LiveEvent, TranscriptFinalPayload, TranscriptTurn } from '@/lib/types'
import { callKey, mergeTurns, type CallDetail } from './api'
import { eventCallId } from './eventUtils'

type EndedExtras = CallEventPayload & { durationSec?: number; llmModelUsed?: string }

function mergeCall(old: Call, ev: LiveEvent): Call {
  const p = ev.payload as EndedExtras
  const next: Call = { ...old, ...(p.call ?? {}) }
  if (ev.type === 'call.ended') {
    if (!next.summary && p.summary) next.summary = p.summary
    if (!next.sentiment && p.sentiment) next.sentiment = p.sentiment
    if (!next.intent && p.intent) next.intent = p.intent
    if (!next.endReason && p.endReason) next.endReason = p.endReason
    if (!next.llmModelUsed && p.llmModelUsed) next.llmModelUsed = p.llmModelUsed
    if (!next.durationSec && p.durationSec) next.durationSec = p.durationSec
  }
  return next
}

/** Apply a live event to the cached `GET /api/calls/{id}` response. Exported for tests. */
export function applyEventToCache(qc: QueryClient, callId: string, ev: LiveEvent): void {
  const key = callKey(callId)
  if (ev.type === 'transcript.final') {
    const { turn } = ev.payload as TranscriptFinalPayload
    qc.setQueryData<CallDetail>(key, (old) => (old ? { ...old, turns: mergeTurns(old.turns, [turn]) } : old))
  } else if (ev.type.startsWith('call.')) {
    qc.setQueryData<CallDetail>(key, (old) => (old ? { ...old, call: mergeCall(old.call, ev) } : old))
    // Summary, sentiment and the recording URL are filled in asynchronously.
    if (ev.type === 'call.ended' || ev.type === 'call.updated') void qc.invalidateQueries({ queryKey: key })
  }
}

/** Keeps the cached call detail in sync with live events for `callId`. */
export function useCallCacheSync(callId: string | null | undefined): void {
  const qc = useQueryClient()
  const onEvent = useLive((s) => s.onEvent)
  useEffect(() => {
    if (!callId) return
    return onEvent((ev) => { if (eventCallId(ev) === callId) applyEventToCache(qc, callId, ev) })
  }, [callId, onEvent, qc])
}

/** Replace one turn inside the cached call detail. */
export function replaceCachedTurn(qc: QueryClient, callId: string, turn: TranscriptTurn): void {
  qc.setQueryData<CallDetail>(callKey(callId), (old) => (old ? { ...old, turns: mergeTurns(old.turns, [turn]) } : old))
}
