import { useState, type FormEvent } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router-dom'
import { AlertCircle, ArrowRight, Eye, EyeOff } from 'lucide-react'
import { useAuth } from '@/app/auth'
import { Button, Card, Field, Input } from '@/components/ui'
import { Logo } from '@/components/layout/Logo'
import { HttpError } from '@/lib/api'

export const DEMO_EMAIL = 'admin@callgo.mn'
export const DEMO_PASSWORD = 'admin1234'

function errorMessage(err: unknown): string {
  if (err instanceof HttpError) {
    if (err.status === 401 || err.status === 400) return 'И-мэйл эсвэл нууц үг буруу байна.'
    if (err.status === 429) return 'Хэт олон оролдлого хийлээ. Түр хүлээгээд дахин оролдоно уу.'
    if (err.status >= 500) return 'Сервер түр ажиллахгүй байна. Дараа дахин оролдоно уу.'
    return err.message || 'Нэвтрэх үед алдаа гарлаа.'
  }
  return 'Сервертэй холбогдож чадсангүй. Сүлжээгээ шалгана уу.'
}

export default function LoginPage() {
  const token = useAuth((s) => s.token)
  const login = useAuth((s) => s.login)
  const navigate = useNavigate()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from ?? '/'

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [showPw, setShowPw] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (token && !loading) return <Navigate to={from} replace />

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!email.trim() || !password) { setError('И-мэйл болон нууц үгээ оруулна уу.'); return }
    setLoading(true)
    setError(null)
    try {
      await login(email, password)
      navigate(from === '/login' ? '/' : from, { replace: true })
    } catch (err) {
      setError(errorMessage(err))
      setLoading(false)
    }
  }

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden bg-[var(--surface-0)] px-4 py-12">
      {/* Soft accent glow + grid, kept subtle. */}
      <div aria-hidden className="pointer-events-none absolute inset-0 bg-[radial-gradient(600px_300px_at_50%_-60px,var(--accent-soft),transparent)]" />
      <div aria-hidden className="pointer-events-none absolute inset-0 opacity-[0.35] [background-image:linear-gradient(var(--border-subtle)_1px,transparent_1px),linear-gradient(90deg,var(--border-subtle)_1px,transparent_1px)] [background-size:48px_48px] [mask-image:radial-gradient(ellipse_at_center,black_20%,transparent_70%)]" />

      <div className="relative w-full max-w-[380px]">
        <div className="mb-8 flex flex-col items-center text-center">
          <Logo />
          <h1 className="mt-6 text-lg font-semibold tracking-[-0.015em] text-[var(--fg)]">Системд нэвтрэх</h1>
          <p className="mt-1 text-[13px] text-[var(--fg-muted)]">AI дуудлагын төвийн удирдлагын самбар</p>
        </div>

        <Card className="p-6 shadow-[var(--shadow-lg)]">
          <form onSubmit={onSubmit} noValidate className="space-y-4" aria-describedby={error ? 'login-error' : undefined}>
            {error && (
              <div id="login-error" role="alert" className="flex items-start gap-2 rounded-[var(--radius-sm)] border border-[var(--danger-border)] bg-[var(--danger-soft)] px-3 py-2 text-xs text-[var(--danger-fg)]">
                <AlertCircle className="mt-px h-3.5 w-3.5 shrink-0" />
                <span>{error}</span>
              </div>
            )}
            <Field label="И-мэйл">
              <Input
                type="email" name="email" autoComplete="username" autoFocus required
                placeholder="name@company.mn" value={email} aria-invalid={!!error || undefined}
                onChange={(e) => setEmail(e.target.value)} className="h-9"
              />
            </Field>
            <Field label="Нууц үг">
              <div className="relative">
                <Input
                  type={showPw ? 'text' : 'password'} name="password" autoComplete="current-password" required
                  placeholder="••••••••" value={password} aria-invalid={!!error || undefined}
                  onChange={(e) => setPassword(e.target.value)} className="h-9 pr-9"
                />
                <button
                  type="button" onClick={() => setShowPw((v) => !v)}
                  aria-label={showPw ? 'Нууц үг нуух' : 'Нууц үг харах'}
                  className="absolute right-1 top-1/2 flex h-7 w-7 -translate-y-1/2 items-center justify-center rounded text-[var(--fg-subtle)] hover:text-[var(--fg)]"
                >
                  {showPw ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                </button>
              </div>
            </Field>
            <Button type="submit" size="lg" className="w-full" loading={loading}>
              {loading ? 'Нэвтэрч байна…' : <>Нэвтрэх <ArrowRight /></>}
            </Button>
          </form>
        </Card>

        <button
          type="button"
          onClick={() => { setEmail(DEMO_EMAIL); setPassword(DEMO_PASSWORD); setError(null) }}
          className="mx-auto mt-4 flex items-center gap-1.5 rounded-[var(--radius-sm)] px-2 py-1 text-xs text-[var(--fg-subtle)] transition-colors hover:text-[var(--fg-muted)]"
          title="Демо мэдээллийг бөглөх"
        >
          <span>demo:</span>
          <span className="mono text-[var(--fg-muted)]">{DEMO_EMAIL} / {DEMO_PASSWORD}</span>
        </button>
        <p className="mt-8 text-center text-[11px] text-[var(--fg-subtle)]">© {new Date().getFullYear()} CallGo.mn</p>
      </div>
    </div>
  )
}
