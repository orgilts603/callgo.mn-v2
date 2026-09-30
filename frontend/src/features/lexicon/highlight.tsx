import type { ReactNode } from 'react'

export interface LexiconHit { id: string; wrong: string; correct: string }

function escapeRegExp(s: string): string { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') }

/** Renders `text` with every occurrence of a hit's corrected word wrapped in <mark>. */
export function highlightHits(text: string, hits: LexiconHit[]): ReactNode[] {
  const words = Array.from(new Set(hits.map((h) => h.correct).filter(Boolean))).sort((a, b) => b.length - a.length)
  if (words.length === 0) return [text]
  const re = new RegExp(`(${words.map(escapeRegExp).join('|')})`, 'giu')
  return text.split(re).map((part, i) =>
    i % 2 === 1
      ? <mark key={i} className="rounded bg-amber-400/30 px-0.5 text-[var(--fg)]">{part}</mark>
      : part,
  )
}
