import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useLive } from '@/lib/ws'
import {
  TERMINAL_STATUSES, type AgentStatePayload, type Call, type LiveEvent, type TranscriptFinalPayload, type TranscriptPartialPayload,
} from '@/lib/types'
import { eventCallId } from '@/features/calls/eventUtils'
import { activeCallsKey } from '@/features/calls/api'
import type { CallLiveMeta } from './types'

export const FEED_LIMIT = 50

export interface LiveDeskEvents {
  /** Per-call agent state and last transcript snippet. */
  meta: Record<string, CallLiveMeta>
  /** Most recent events, newest first (partials excluded — they are too chatty for a feed). */
  feed: LiveEvent[]
  /** Calls seen ending over the socket (so a stale REST snapshot cannot resurrect them). */
  endedIds: ReadonlySet<string>
}

const EMPTY_SET: ReadonlySet<string> = new Set()

/** Pure meta reducer; exported for tests. */
export function reduceMeta(meta: Record<string, CallLiveMeta>, ev: LiveEvent): Record<string, CallLiveMeta> {
  const id = eventCallId(ev)
  if (!id) return meta
  const cur = meta[id] ?? {}
  switch (ev.type) {
    case 'transcript.partial': {
      const p = ev.payload as TranscriptPartialPayload
      if (!p.text.trim()) return meta
      return { ...meta, [id]: { ...cur, snippet: { speaker: p.speaker, text: p.text, partial: true } } }
    }
    case 'transcript.final': {
      const { turn } = ev.payload as TranscriptFinalPayload
      return { ...meta, [id]: { ...cur, snippet: { speaker: turn.speaker, text: turn.text, partial: false } } }
    }
    case 'agent.state':
      return { ...meta, [id]: { ...cur, agentState: (ev.payload as AgentStatePayload).state } }
    case 'call.ended': {
      if (!(id in meta)) return meta
      const next = { ...meta }
      delete next[id]
      return next
    }
    default:
      return meta
  }
}

function isEnding(ev: LiveEvent): boolean {
  if (ev.type === 'call.ended') return true
  if (!ev.type.startsWith('call.')) return false
  const call = (ev.payload as { call?: Call } | null)?.call
  return !!call && TERMINAL_STATUSES.includes(call.status)
}

/** Single socket listener feeding the Live Desk: per-call meta, the event feed and ended ids. */
export function useLiveDeskEvents(): LiveDeskEvents {
  const onEvent = useLive((s) => s.onEvent)
  const status = useLive((s) => s.status)
  const qc = useQueryClient()
  const [meta, setMeta] = useState<Record<string, CallLiveMeta>>({})
  const [feed, setFeed] = useState<LiveEvent[]>([])
  const [endedIds, setEndedIds] = useState<ReadonlySet<string>>(EMPTY_SET)
  const statsTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    const off = onEvent((ev) => {
      setMeta((m) => reduceMeta(m, ev))
      if (ev.type !== 'transcript.partial') setFeed((f) => [ev, ...f].slice(0, FEED_LIMIT))
      if (isEnding(ev)) {
        const id = eventCallId(ev)
        if (id) setEndedIds((s) => (s.has(id) ? s : new Set(s).add(id)))
      }
      if (ev.type === 'call.ended' || ev.type === 'call.started') {
        // Coalesce bursts (e.g. a campaign finishing) into one stats refetch.
        if (statsTimer.current) clearTimeout(statsTimer.current)
        statsTimer.current = setTimeout(() => { void qc.invalidateQueries({ queryKey: ['stats'] }) }, 1500)
      }
    })
    return () => {
      off()
      if (statsTimer.current) clearTimeout(statsTimer.current)
    }
  }, [onEvent, qc])

  // After a (re)connect the REST snapshot may be stale; refresh it.
  useEffect(() => {
    if (status === 'open') void qc.invalidateQueries({ queryKey: activeCallsKey })
  }, [status, qc])

  return { meta, feed, endedIds }
}
