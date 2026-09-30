import { HttpError } from '@/lib/api'

export function errMsg(e: unknown): string {
  if (e instanceof HttpError) {
    if (e.code === 'payment_required') return 'Төлбөр төлөгдөөгүй тул үйлдэл хязгаарлагдсан байна'
    if (e.code === 'quota_exceeded') return 'Багцын хязгаар хэтэрсэн байна'
    return e.message || 'Алдаа гарлаа'
  }
  return e instanceof Error && e.message ? e.message : 'Алдаа гарлаа'
}
