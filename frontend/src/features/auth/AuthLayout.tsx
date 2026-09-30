import { useState, type InputHTMLAttributes, type ReactNode } from 'react'
import { AlertCircle, CheckCircle2, Eye, EyeOff } from 'lucide-react'
import { Card, Input } from '@/components/ui'
import { Logo } from '@/components/layout/Logo'
import { HttpError } from '@/lib/api'
import { cn } from '@/lib/utils'

const YEAR = new Date().getFullYear()

export const linkCls = 'font-medium text-[var(--fg-muted)] underline-offset-2 transition-colors hover:text-[var(--fg)] hover:underline'

/** Centered public page (login, signup, reset…): soft glow + grid, logo, title and a card. */
export function AuthLayout({ title, subtitle, children, footer, wide }: {
  title: ReactNode; subtitle?: ReactNode; children: ReactNode; footer?: ReactNode; wide?: boolean
}) {
  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden bg-[var(--surface-0)] px-4 py-12">
      {/* Soft accent glow + grid, kept subtle. */}
      <div aria-hidden className="pointer-events-none absolute inset-0 bg-[radial-gradient(600px_300px_at_50%_-60px,var(--accent-soft),transparent)]" />
      <div aria-hidden className="pointer-events-none absolute inset-0 opacity-[0.35] [background-image:linear-gradient(var(--border-subtle)_1px,transparent_1px),linear-gradient(90deg,var(--border-subtle)_1px,transparent_1px)] [background-size:48px_48px] [mask-image:radial-gradient(ellipse_at_center,black_20%,transparent_70%)]" />

      <div className={cn('relative w-full', wide ? 'max-w-[440px]' : 'max-w-[380px]')}>
        <div className="mb-8 flex flex-col items-center text-center">
          <Logo />
          <h1 className="mt-6 text-lg font-semibold tracking-[-0.015em] text-[var(--fg)]">{title}</h1>
          {subtitle && <p className="mt-1 text-[13px] text-[var(--fg-muted)]">{subtitle}</p>}
        </div>
        <Card className="p-6 shadow-[var(--shadow-lg)]">{children}</Card>
        {footer}
        <p className="mt-8 text-center text-[11px] text-[var(--fg-subtle)]">© {YEAR} CallGo.mn</p>
      </div>
    </div>
  )
}

export function FormAlert({ id, children, tone = 'danger' }: { id?: string; children: ReactNode; tone?: 'danger' | 'success' }) {
  const Icon = tone === 'danger' ? AlertCircle : CheckCircle2
  return (
    <div id={id} role={tone === 'danger' ? 'alert' : 'status'}
      className={cn(
        'flex items-start gap-2 rounded-[var(--radius-sm)] border px-3 py-2 text-xs',
        tone === 'danger'
          ? 'border-[var(--danger-border)] bg-[var(--danger-soft)] text-[var(--danger-fg)]'
          : 'border-[var(--success-border)] bg-[var(--success-soft)] text-[var(--success-fg)]',
      )}>
      <Icon className="mt-px h-3.5 w-3.5 shrink-0" />
      <span>{children}</span>
    </div>
  )
}

/** Password input with a show/hide toggle. */
export function PasswordInput({ className, ...rest }: InputHTMLAttributes<HTMLInputElement>) {
  const [show, setShow] = useState(false)
  return (
    <div className="relative">
      <Input {...rest} type={show ? 'text' : 'password'} className={cn('h-9 pr-9', className)} />
      <button
        type="button" onClick={() => setShow((v) => !v)}
        aria-label={show ? 'Нууц үг нуух' : 'Нууц үг харах'}
        className="absolute right-1 top-1/2 flex h-7 w-7 -translate-y-1/2 items-center justify-center rounded text-[var(--fg-subtle)] hover:text-[var(--fg)]"
      >
        {show ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
      </button>
    </div>
  )
}

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

const strength = [
  { label: '', bar: '' },
  { label: 'Сул', bar: 'bg-[var(--danger)]' },
  { label: 'Дунд зэрэг', bar: 'bg-[var(--warning)]' },
  { label: 'Сайн', bar: 'bg-[var(--success)]' },
  { label: 'Хүчтэй', bar: 'bg-[var(--success)]' },
] as const

/** Four-segment strength meter + hint shown under a new-password field. */
export function PasswordStrength({ password }: { password: string }) {
  const score = passwordScore(password)
  const hint = password.length < MIN_PASSWORD
    ? `Хамгийн багадаа ${MIN_PASSWORD} тэмдэгт`
    : score < 3 ? 'Том, жижиг үсэг, тоо, тэмдэгт холивол илүү найдвартай' : 'Сайн нууц үг'
  return (
    <div className="space-y-1" data-testid="password-strength" data-score={score}>
      <div className="flex gap-1" aria-hidden>
        {[1, 2, 3, 4].map((i) => (
          <span key={i} className={cn('h-1 flex-1 rounded-full bg-[var(--surface-3)]', score >= i && strength[score].bar)} />
        ))}
      </div>
      <div className="flex justify-between text-[11px] text-[var(--fg-subtle)]">
        <span>{hint}</span>
        {score > 0 && <span className="font-medium text-[var(--fg-muted)]">{strength[score].label}</span>}
      </div>
    </div>
  )
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

/** Small full-width status block used by the token pages (verify / reset / invite). */
export function StatusBlock({ icon, title, children }: { icon: ReactNode; title: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 py-2 text-center">
      <div className="mb-1 flex h-10 w-10 items-center justify-center rounded-full border border-[var(--border)] bg-[var(--surface-2)] [&_svg]:size-[18px]">{icon}</div>
      <div className="text-sm font-medium text-[var(--fg)]">{title}</div>
      {children && <div className="text-xs leading-relaxed text-[var(--fg-muted)]">{children}</div>}
    </div>
  )
}
