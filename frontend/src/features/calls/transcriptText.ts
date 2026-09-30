import type { LexiconScope, TranscriptTurn } from '@/lib/types'
import type { TurnPatchBody } from './api'

export interface TextToken {
  /** Raw token text (a word with its punctuation, or a whitespace run). */
  text: string
  /** Index among word tokens, or -1 for whitespace. */
  wordIndex: number
}

/** Split text into alternating word / whitespace tokens, preserving the original spacing. */
export function tokenize(text: string): TextToken[] {
  const out: TextToken[] = []
  let w = 0
  for (const part of text.split(/(\s+)/)) {
    if (!part) continue
    if (/^\s+$/.test(part)) out.push({ text: part, wordIndex: -1 })
    else out.push({ text: part, wordIndex: w++ })
  }
  return out
}

const EDGE_PUNCT = /^([\p{P}\p{S}]*)(.*?)([\p{P}\p{S}]*)$/su

/** Split a word token into leading punctuation, the word core, and trailing punctuation. */
export function splitPunct(token: string): { lead: string; core: string; trail: string } {
  const m = EDGE_PUNCT.exec(token)
  if (!m || !m[2]) return { lead: '', core: token, trail: '' }
  return { lead: m[1], core: m[2], trail: m[3] }
}

/** The bare word (without surrounding punctuation) at `wordIndex`, or '' if out of range. */
export function wordAt(text: string, wordIndex: number): string {
  const tok = tokenize(text).find((t) => t.wordIndex === wordIndex)
  return tok ? splitPunct(tok.text).core : ''
}

/** Replace only the word at `wordIndex` (keeping its punctuation and all spacing). */
export function replaceWordAt(text: string, wordIndex: number, replacement: string): string {
  return tokenize(text)
    .map((t) => {
      if (t.wordIndex !== wordIndex) return t.text
      const { lead, trail } = splitPunct(t.text)
      return `${lead}${replacement}${trail}`
    })
    .join('')
}

export interface CorrectionInput { correct: string; scope: LexiconScope; phonetic?: string }

/** Build the PATCH /api/turns/{id} body for a single-word lexicon correction. */
export function buildCorrectionBody(turn: Pick<TranscriptTurn, 'text'>, wordIndex: number, input: CorrectionInput): TurnPatchBody {
  const wrong = wordAt(turn.text, wordIndex)
  const correct = input.correct.trim()
  const body: TurnPatchBody = { text: replaceWordAt(turn.text, wordIndex, correct), wrong, correct, scope: input.scope }
  const phonetic = input.phonetic?.trim()
  if (phonetic) body.phonetic = phonetic
  return body
}
