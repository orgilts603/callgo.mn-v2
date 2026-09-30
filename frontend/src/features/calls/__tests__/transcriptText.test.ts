import { buildCorrectionBody, replaceWordAt, splitPunct, tokenize, wordAt } from '../transcriptText'
import { mergeTurns } from '../api'
import { makeTurn } from '../testUtils'

describe('transcriptText', () => {
  const text = 'Сайн байна уу,  би колгоу захиалъя.'

  it('tokenizes words and keeps whitespace', () => {
    const toks = tokenize(text)
    expect(toks.map((t) => t.text).join('')).toBe(text)
    expect(toks.filter((t) => t.wordIndex >= 0).map((t) => t.text)).toEqual(['Сайн', 'байна', 'уу,', 'би', 'колгоу', 'захиалъя.'])
  })

  it('strips surrounding punctuation from the clicked word', () => {
    expect(splitPunct('«колгоу»,')).toEqual({ lead: '«', core: 'колгоу', trail: '»,' })
    expect(wordAt(text, 2)).toBe('уу')
    expect(wordAt(text, 5)).toBe('захиалъя')
    expect(wordAt(text, 99)).toBe('')
  })

  it('replaces only the word at the index, preserving punctuation and spacing', () => {
    expect(replaceWordAt('колгоу, колгоу.', 1, 'CallGo')).toBe('колгоу, CallGo.')
    expect(replaceWordAt(text, 4, 'CallGo')).toBe('Сайн байна уу,  би CallGo захиалъя.')
  })

  it('builds the PATCH body', () => {
    const body = buildCorrectionBody(makeTurn({ text }), 4, { correct: ' CallGo ', scope: 'both', phonetic: ' кол гоу ' })
    expect(body).toEqual({ text: 'Сайн байна уу,  би CallGo захиалъя.', wrong: 'колгоу', correct: 'CallGo', scope: 'both', phonetic: 'кол гоу' })
    expect(buildCorrectionBody(makeTurn({ text }), 4, { correct: 'CallGo', scope: 'stt', phonetic: '' })).not.toHaveProperty('phonetic')
  })

  it('merges turns by id and sorts by seq', () => {
    const a = makeTurn({ id: 'a', seq: 2 })
    const b = makeTurn({ id: 'b', seq: 1 })
    const a2 = makeTurn({ id: 'a', seq: 2, text: 'updated' })
    const merged = mergeTurns([a], [b, a2])
    expect(merged.map((t) => t.id)).toEqual(['b', 'a'])
    expect(merged[1].text).toBe('updated')
  })
})
