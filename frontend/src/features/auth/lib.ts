// Non-component helpers shared by the public auth pages.
import { HttpError } from '@/lib/api'

export const linkCls = 'font-medium text-[var(--fg-muted)] underline-offset-2 transition-colors hover:text-[var(--fg)] hover:underline'

export const MIN_PASSWORD = 8

/** 0 (empty/too short) … 4 (strong). Length is the main driver; character variety adds points. */
export function passwordScore(pw: string): 0 | 1 | 2 | 3 | 4 {
  if (pw.length < MIN_PASSWORD) return pw.length ? 1 : 0
  let variety = 0
  if (/[a-zа-яөү]/.test(pw)) variety++
  if (/[A-ZА-ЯӨҮ]/.test(pw)) variety++
  if (/\d/.test(pw)) variety++
  if (/[^\p{L}\d]/u.test(pw)) variety++
  let score = 1
  if (variety >= 2) score++
  if (variety >= 3 || pw.length >= 12) score++
  if (variety >= 3 && pw.length >= 12) score++
  return Math.min(4, score) as 1 | 2 | 3 | 4
}

/** Returns an error message, or null when the password is acceptable. */
export function validateNewPassword(pw: string, confirm?: string): string | null {
  if (pw.length < MIN_PASSWORD) return `Нууц үг хамгийн багадаа ${MIN_PASSWORD} тэмдэгт байна.`
  if (confirm !== undefined && pw !== confirm) return 'Нууц үг таарахгүй байна.'
  return null
}

export function isValidEmail(email: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())
}

/** Maps API failures of the public auth endpoints to Mongolian copy; `overrides` wins per status. */
export function authErrorMessage(err: unknown, overrides: Partial<Record<number, string>> = {}): string {
  if (err instanceof HttpError) {
    if (overrides[err.status]) return overrides[err.status] as string
    if (err.status === 429) return 'Хэт олон оролдлого хийлээ. Түр хүлээгээд дахин оролдоно уу.'
    if (err.status >= 500) return 'Сервер түр ажиллахгүй байна. Дараа дахин оролдоно уу.'
    return err.message || 'Алдаа гарлаа. Дахин оролдоно уу.'
  }
  return 'Сервертэй холбогдож чадсангүй. Сүлжээгээ шалгана уу.'
}
