import type { ReactNode } from 'react'
import { Sparkles } from 'lucide-react'
import { Card, SentimentBadge } from '@/components/ui'
import type { Call } from '@/lib/types'
import { END_REASON_LABEL } from './callLabels'
import { isLiveStatus } from './api'

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] uppercase tracking-wide text-[var(--fg-subtle)]">{label}</dt>
      <dd className="mt-0.5 truncate text-sm text-[var(--fg)]">{children}</dd>
    </div>
  )
}

/** LLM post-call summary: summary text, sentiment, intent, model and end reason. */
export function SummaryCard({ call, liveModel }: { call: Call; liveModel?: string | null }) {
  const live = isLiveStatus(call.status)
  const model = call.llmModelUsed || liveModel || ''
  return (
    <Card className="px-4 py-3.5" data-testid="summary-card">
      <div className="mb-2 flex items-center gap-2 text-xs font-semibold text-[var(--fg)]">
        <Sparkles className="h-3.5 w-3.5 text-[var(--accent-fg)]" /> Дүгнэлт
      </div>
      {call.summary
        ? <p className="whitespace-pre-line text-sm leading-relaxed text-[var(--fg)]">{call.summary}</p>
        : <p className="text-sm text-[var(--fg-subtle)]">{live ? 'Дуудлага дууссаны дараа дүгнэлт гарна.' : 'Дүгнэлт гараагүй байна.'}</p>}
      <dl className="mt-3 grid grid-cols-2 gap-3 border-t border-[var(--border)] pt-3 sm:grid-cols-4">
        <Item label="Сэтгэл хандлага"><SentimentBadge sentiment={call.sentiment} /></Item>
        <Item label="Зорилго">{call.intent || '—'}</Item>
        <Item label="LLM загвар"><span className="font-mono text-xs" title={model}>{model || '—'}</span></Item>
        <Item label="Дууссан шалтгаан">{call.endReason ? (END_REASON_LABEL[call.endReason] ?? call.endReason) : '—'}</Item>
      </dl>
    </Card>
  )
}
