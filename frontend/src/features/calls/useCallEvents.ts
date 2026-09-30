import { useEffect, useState } from 'react'
import { useLive } from '@/lib/ws'
import type {
  AgentState, AgentStatePayload, LiveEvent, Speaker, TranscriptFinalPayload, TranscriptPartialPayload, TranscriptTurn,
} from '@/lib/types'
import { eventCallId } from './eventUtils'

export interface CallEventsState {
  /** In-flight (non-final) utterances, at most one per speaker. */
  partials: TranscriptPartialPayload[]
  /** Latest `agent.state` for the call, or null if none received yet. */
  agentState: AgentState | null
  /** LLM model reported alongside the latest agent state. */
  llmModel: string | null
  /** Latest `transcript.final` turn received for the call. */
  lastTurn: TranscriptTurn | null
}

interface Internal extends CallEventsState { callId: string | null }

const NO_PARTIALS: TranscriptPartialPayload[] = []
const initial = (callId: string | null): Internal => ({ callId, partials: NO_PARTIALS, agentState: null, llmModel: null, lastTurn: null })

function withoutSpeaker(list: TranscriptPartialPayload[], speaker: Speaker): TranscriptPartialPayload[] {
  return list.some((p) => p.speaker === speaker) ? list.filter((p) => p.speaker !== speaker) : list
}

/** Pure reducer applying a live event to a call's event state. Exported for tests. */
export function reduceCallEvent(state: Internal, ev: LiveEvent): Internal {
  switch (ev.type) {
    case 'transcript.partial': {
      const p = ev.payload as TranscriptPartialPayload
      const rest = withoutSpeaker(state.partials, p.speaker)
      return { ...state, partials: p.text.trim() ? [...rest, p] : rest }
    }
    case 'transcript.final': {
      const { turn } = ev.payload as TranscriptFinalPayload
      return { ...state, lastTurn: turn, partials: withoutSpeaker(state.partials, turn.speaker) }
    }
    case 'agent.state': {
      const p = ev.payload as AgentStatePayload
      return { ...state, agentState: p.state, llmModel: p.llmModel ?? state.llmModel }
    }
    case 'call.ended':
      return { ...state, partials: NO_PARTIALS, agentState: 'idle' }
    default:
      return state
  }
}

/**
 * Subscribes to the live socket for one call and returns the transient state
 * that is not persisted by the API: partial transcripts, agent state and the
 * last final turn. Resets whenever `callId` changes.
 */
export function useCallEvents(callId: string | null | undefined): CallEventsState {
  const onEvent = useLive((s) => s.onEvent)
  const id = callId ?? null
  const [state, setState] = useState<Internal>(() => initial(id))

  useEffect(() => {
    if (!id) return
    return onEvent((ev) => {
      if (eventCallId(ev) !== id) return
      setState((prev) => reduceCallEvent(prev.callId === id ? prev : initial(id), ev))
    })
  }, [id, onEvent])

  const current = state.callId === id ? state : initial(id)
  return { partials: current.partials, agentState: current.agentState, llmModel: current.llmModel, lastTurn: current.lastTurn }
}
