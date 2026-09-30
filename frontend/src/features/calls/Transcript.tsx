import { memo, useCallback, useLayoutEffect, useRef, useState } from 'react'
import { AlertTriangle, ArrowDown, Bot, Headset, MessageSquareDashed, Pencil, User } from 'lucide-react'
import { EmptyState } from '@/components/ui'
import { cn, fmtDuration } from '@/lib/utils'
import type { Speaker, TranscriptPartialPayload, TranscriptTurn } from '@/lib/types'
import { CorrectWordDialog } from './CorrectWordDialog'
import { EditTurnDialog } from './EditTurnDialog'
import { tokenize } from './transcriptText'

/** Turns below this STT confidence are tinted as uncertain. */
export const LOW_CONFIDENCE = 0.6

const speakerMeta: Record<Speaker, { label: string; icon: typeof User; align: 'left' | 'right'; bubble: string }> = {
  customer: { label: 'Харилцагч', icon: User, align: 'left', bubble: 'bg-[var(--surface-2)] border-[var(--border)]' },
  agent: { label: 'AI агент', icon: Bot, align: 'right', bubble: 'bg-[var(--accent)]/12 border-[var(--accent)]/30' },
  human: { label: 'Оператор', icon: Headset, align: 'right', bubble: 'bg-emerald-500/10 border-emerald-500/30' },
}

export interface TranscriptProps {
  callId: string
  turns: TranscriptTurn[]
  partials?: TranscriptPartialPayload[]
  /** Current playback position; the matching turn is highlighted. */
  activeMs?: number | null
  /** Seek the recording to a turn start. */
  onSeek?: (ms: number) => void
  /** Call is still in progress (changes the empty state copy). */
  live?: boolean
  className?: string
}

interface WordTarget { turn: TranscriptTurn; wordIndex: number }

const isLowConfidence = (t: TranscriptTurn) => t.speaker === 'customer' && t.confidence > 0 && t.confidence < LOW_CONFIDENCE

interface TurnBubbleProps {
  turn: TranscriptTurn
  active: boolean
  onSeek?: (ms: number) => void
  onWord: (turn: TranscriptTurn, wordIndex: number) => void
  onEdit: (turn: TranscriptTurn) => void
}

const TurnBubble = memo(function TurnBubble({ turn, active, onSeek, onWord, onEdit }: TurnBubbleProps) {
  const meta = speakerMeta[turn.speaker] ?? speakerMeta.customer
  const Icon = meta.icon
  const low = isLowConfidence(turn)
  const edited = !!turn.rawText && turn.rawText !== turn.text
  const clickableWords = turn.speaker === 'customer'
  return (
    <div className={cn('group flex w-full', meta.align === 'right' ? 'justify-end' : 'justify-start')} data-testid="turn" data-speaker={turn.speaker}>
      <div className={cn('max-w-[85%] space-y-1', meta.align === 'right' && 'items-end text-right')}>
        <div className={cn('flex items-center gap-1.5 text-[11px] text-[var(--fg-subtle)]', meta.align === 'right' && 'justify-end')}>
          <Icon className="h-3 w-3" />
          <span className="font-medium text-[var(--fg-muted)]">{meta.label}</span>
          <button type="button" onClick={() => onSeek?.(turn.startMs)} disabled={!onSeek}
            className="tabular-nums hover:text-[var(--accent-fg)] disabled:cursor-default disabled:hover:text-inherit" title="Бичлэгийг энд аваачих">
            {fmtDuration(turn.startMs / 1000)}
          </button>
          {low && (
            <span className="inline-flex items-center gap-0.5 text-amber-300" title={`Итгэлцүүр: ${Math.round(turn.confidence * 100)}%`}>
              <AlertTriangle className="h-3 w-3" />{Math.round(turn.confidence * 100)}%
            </span>
          )}
          {edited && <span className="text-[var(--fg-subtle)]" title={`STT эх: ${turn.rawText}`}>· засварласан</span>}
          <button type="button" onClick={() => onEdit(turn)} aria-label="Мөр засах"
            className="rounded p-0.5 opacity-0 transition-opacity hover:bg-[var(--surface-3)] hover:text-[var(--fg)] focus-visible:opacity-100 group-hover:opacity-100">
            <Pencil className="h-3 w-3" />
          </button>
        </div>
        <div
          onClick={clickableWords ? undefined : () => onSeek?.(turn.startMs)}
          className={cn(
            'rounded-2xl border px-3.5 py-2 text-left text-sm leading-relaxed text-[var(--fg)] transition-shadow',
            meta.bubble,
            meta.align === 'right' ? 'rounded-tr-sm' : 'rounded-tl-sm',
            low && 'border-amber-500/50 bg-amber-500/10',
            active && 'ring-2 ring-[var(--accent)]/70',
            !clickableWords && onSeek && 'cursor-pointer',
          )}
        >
          {clickableWords
            ? tokenize(turn.text).map((tok, i) => tok.wordIndex < 0 ? tok.text : (
              <button key={i} type="button" onClick={() => onWord(turn, tok.wordIndex)} title="Үгийг засах"
                className="-mx-px cursor-pointer rounded px-px text-left underline-offset-4 hover:bg-[var(--accent)]/20 hover:underline decoration-dotted focus-visible:bg-[var(--accent)]/20 focus-visible:outline-none">
                {tok.text}
              </button>
            ))
            : turn.text}
        </div>
      </div>
    </div>
  )
})

function PartialBubble({ partial }: { partial: TranscriptPartialPayload }) {
  const meta = speakerMeta[partial.speaker] ?? speakerMeta.customer
  return (
    <div className={cn('flex w-full', meta.align === 'right' ? 'justify-end' : 'justify-start')} data-testid="partial">
      <div className={cn('max-w-[85%] rounded-2xl border border-dashed px-3.5 py-2 text-sm italic leading-relaxed text-[var(--fg-muted)] opacity-70', meta.bubble)}>
        {partial.text}
        <span className="ml-1 inline-flex gap-0.5 align-middle">
          <span className="h-1 w-1 animate-pulse rounded-full bg-current" />
          <span className="h-1 w-1 animate-pulse rounded-full bg-current [animation-delay:150ms]" />
          <span className="h-1 w-1 animate-pulse rounded-full bg-current [animation-delay:300ms]" />
        </span>
      </div>
    </div>
  )
}

const BOTTOM_SLACK = 48
const NO_PARTIALS: TranscriptPartialPayload[] = []

/** Chat-style transcript with live partials, auto-scroll and word-level correction. */
export function Transcript({ callId, turns, partials = NO_PARTIALS, activeMs, onSeek, live, className }: TranscriptProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const stickRef = useRef(true)
  const [atBottom, setAtBottom] = useState(true)
  const [wordTarget, setWordTarget] = useState<WordTarget | null>(null)
  const [editTarget, setEditTarget] = useState<TranscriptTurn | null>(null)

  const onWord = useCallback((turn: TranscriptTurn, wordIndex: number) => setWordTarget({ turn, wordIndex }), [])
  const onEdit = useCallback((turn: TranscriptTurn) => setEditTarget(turn), [])

  const scrollToBottom = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    stickRef.current = true
    setAtBottom(true)
  }, [])

  const onScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_SLACK
    stickRef.current = bottom
    setAtBottom(bottom)
  }, [])

  const lastPartial = partials.length ? partials[partials.length - 1].text : ''
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (el && stickRef.current) el.scrollTop = el.scrollHeight
  }, [turns.length, partials.length, lastPartial])

  const empty = turns.length === 0 && partials.length === 0
  return (
    <div className={cn('relative', className)}>
      <div ref={scrollRef} onScroll={onScroll} role="log" aria-live="polite" aria-label="Яриа"
        className="h-full space-y-3 overflow-y-auto px-4 py-4">
        {empty ? (
          <EmptyState icon={<MessageSquareDashed className="h-7 w-7" />}
            title={live ? 'Яриа эхлэхийг хүлээж байна' : 'Яриа бичигдээгүй'}
            description={live ? 'Шинэ мөрүүд энд шууд гарна.' : undefined} className="py-10" />
        ) : (
          <>
            {turns.map((t) => (
              <TurnBubble key={t.id || `seq-${t.seq}`} turn={t} onSeek={onSeek} onWord={onWord} onEdit={onEdit}
                active={activeMs != null && activeMs >= t.startMs && activeMs < Math.max(t.endMs, t.startMs + 1)} />
            ))}
            {partials.map((p) => <PartialBubble key={`partial-${p.speaker}`} partial={p} />)}
          </>
        )}
      </div>
      {!atBottom && !empty && (
        <button type="button" onClick={scrollToBottom}
          className="absolute bottom-3 left-1/2 inline-flex -translate-x-1/2 items-center gap-1 rounded-full border border-[var(--border)] bg-[var(--surface-3)] px-3 py-1 text-xs text-[var(--fg)] shadow-lg hover:bg-[var(--surface-2)]">
          <ArrowDown className="h-3 w-3" /> Доош гүйлгэх
        </button>
      )}
      {wordTarget && (
        <CorrectWordDialog key={`${wordTarget.turn.id}:${wordTarget.wordIndex}`} open callId={callId}
          turn={wordTarget.turn} wordIndex={wordTarget.wordIndex} onClose={() => setWordTarget(null)} />
      )}
      {editTarget && (
        <EditTurnDialog key={editTarget.id} open callId={callId} turn={editTarget} onClose={() => setEditTarget(null)} />
      )}
    </div>
  )
}
