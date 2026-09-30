import { useState, type FormEvent } from 'react'
import { Link, Navigate, useLocation, useNavigate } from 'react-router-dom'
import { ArrowRight } from 'lucide-react'
import { useAuth } from '@/app/auth'
import { Button, Field, Input } from '@/components/ui'
import { HttpError } from '@/lib/api'
import { AuthLayout, FormAlert, linkCls, PasswordInput } from './AuthLayout'

export const DEMO_EMAIL = 'admin@callgo.mn'
export const DEMO_PASSWORD = 'admin1234'

function errorMessage(err: unknown): string {
  if (err instanceof HttpError) {
    if (err.status === 401 || err.status === 400) return 'И-мэйл эсвэл нууц үг буруу байна.'
    if (err.status === 403) return err.message || 'Энэ бүртгэл идэвхгүй болсон байна.'
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
    <AuthLayout
      title="Системд нэвтрэх"
      subtitle="AI дуудлагын төвийн удирдлагын самбар"
      footer={
        <>
          <p className="mt-5 text-center text-xs text-[var(--fg-subtle)]">
            Бүртгэл байхгүй юу? <Link to="/signup" className={linkCls}>Бүртгүүлэх</Link>
          </p>
          <button
            type="button"
            onClick={() => { setEmail(DEMO_EMAIL); setPassword(DEMO_PASSWORD); setError(null) }}
            className="mx-auto mt-3 flex items-center gap-1.5 rounded-[var(--radius-sm)] px-2 py-1 text-xs text-[var(--fg-subtle)] transition-colors hover:text-[var(--fg-muted)]"
            title="Демо мэдээллийг бөглөх"
          >
            <span>demo:</span>
            <span className="mono text-[var(--fg-muted)]">{DEMO_EMAIL} / {DEMO_PASSWORD}</span>
          </button>
        </>
      }
    >
      <form onSubmit={onSubmit} noValidate className="space-y-4" aria-describedby={error ? 'login-error' : undefined}>
        {error && <FormAlert id="login-error">{error}</FormAlert>}
        <Field label="И-мэйл">
          <Input
            type="email" name="email" autoComplete="username" autoFocus required
            placeholder="name@company.mn" value={email} aria-invalid={!!error || undefined}
            onChange={(e) => setEmail(e.target.value)} className="h-9"
          />
        </Field>
        <div className="space-y-1.5">
          <Field label="Нууц үг">
            <PasswordInput
              name="password" autoComplete="current-password" required
              placeholder="••••••••" value={password} aria-invalid={!!error || undefined}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          <div className="flex justify-end">
            <Link to="/forgot-password" className={`text-xs ${linkCls}`}>Нууц үг мартсан?</Link>
          </div>
        </div>
        <Button type="submit" size="lg" className="w-full" loading={loading}>
          {loading ? 'Нэвтэрч байна…' : <>Нэвтрэх <ArrowRight /></>}
        </Button>
      </form>
    </AuthLayout>
  )
}
