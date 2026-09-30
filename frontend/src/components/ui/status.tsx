import { Badge, type BadgeTone } from './badge'
import type { AgentState, CallStatus, CampaignStatus, Sentiment } from '@/lib/types'

const callTone: Record<CallStatus, BadgeTone> = {
  queued: 'neutral', ringing: 'warning', active: 'success', completed: 'neutral', failed: 'danger', no_answer: 'neutral', busy: 'warning', voicemail: 'neutral',
}
const callLabel: Record<CallStatus, string> = {
  queued: 'Дараалалд', ringing: 'Дуугарч байна', active: 'Ярьж байна', completed: 'Дууссан', failed: 'Амжилтгүй', no_answer: 'Хариулаагүй', busy: 'Завгүй', voicemail: 'Дуут шуудан',
}
export function CallStatusBadge({ status }: { status: CallStatus }) {
  const live = status === 'active' || status === 'ringing'
  return <Badge tone={callTone[status]} dot pulse={live}>{callLabel[status]}</Badge>
}
const sentTone: Record<Exclude<Sentiment, ''>, BadgeTone> = { positive: 'success', neutral: 'neutral', negative: 'danger' }
const sentLabel: Record<Exclude<Sentiment, ''>, string> = { positive: 'Эерэг', neutral: 'Төвийг сахисан', negative: 'Сөрөг' }
export function SentimentBadge({ sentiment }: { sentiment?: Sentiment }) {
  if (!sentiment) return <span className="text-xs text-[var(--fg-subtle)]">—</span>
  return <Badge tone={sentTone[sentiment]}>{sentLabel[sentiment]}</Badge>
}
const campTone: Record<CampaignStatus, BadgeTone> = { draft: 'neutral', running: 'success', paused: 'warning', completed: 'neutral' }
const campLabel: Record<CampaignStatus, string> = { draft: 'Ноорог', running: 'Ажиллаж байна', paused: 'Түр зогссон', completed: 'Дууссан' }
export function CampaignStatusBadge({ status }: { status: CampaignStatus }) {
  return <Badge tone={campTone[status]} dot pulse={status === 'running'}>{campLabel[status]}</Badge>
}
const agentTone: Record<AgentState, BadgeTone> = { initializing: 'neutral', listening: 'neutral', thinking: 'warning', speaking: 'success', idle: 'neutral', handoff: 'warning' }
const agentLabel: Record<AgentState, string> = { initializing: 'Бэлдэж байна', listening: 'Сонсож байна', thinking: 'Бодож байна', speaking: 'Ярьж байна', idle: 'Хүлээж байна', handoff: 'Оператор' }
export function AgentStateBadge({ state }: { state: AgentState }) {
  return <Badge tone={agentTone[state]} dot pulse={state === 'speaking' || state === 'thinking'}>{agentLabel[state]}</Badge>
}
