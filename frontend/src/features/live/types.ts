import type { AgentState, Call, Speaker } from '@/lib/types'

export interface TranscriptSnippet { speaker: Speaker; text: string; partial: boolean }

/** One row of the Live Desk table. Rows are reused (same reference) while their fields are unchanged. */
export interface CallRow {
  id: string
  call: Call
  contactName: string | null
  campaignName: string | null
  agentState: AgentState | null
  snippet: TranscriptSnippet | null
}

export interface CallLiveMeta { agentState?: AgentState; snippet?: TranscriptSnippet }
