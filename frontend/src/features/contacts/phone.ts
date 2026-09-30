/** Normalises user input to E.164. 8 local Mongolian digits get the +976 prefix. Returns null when invalid. */
export function normalizePhone(input: string): string | null {
  let s = input.trim().replace(/[\s()-]/g, '')
  if (!s) return null
  if (s.startsWith('00')) s = `+${s.slice(2)}`
  if (/^\d{8}$/.test(s)) s = `+976${s}`
  else if (/^976\d{8}$/.test(s)) s = `+${s}`
  if (!/^\+[1-9]\d{7,14}$/.test(s)) return null
  if (s.startsWith('+976') && s.length !== 12) return null
  return s
}

export function parseTags(input: string): string[] {
  return Array.from(new Set(input.split(',').map((t) => t.trim()).filter(Boolean)))
}
