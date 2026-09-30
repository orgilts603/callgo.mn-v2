import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { CheckCircle2, XCircle } from 'lucide-react'
import { Button, Field } from '@/components/ui'
import { api, HttpError } from '@/lib/api'
import { AuthLayout, FormAlert, PasswordInput, PasswordStrength, StatusBlock } from './AuthLayout'
import { authErrorMessage, linkCls, validateNewPassword } from './lib'

const INVALID_LINK = 'Сэргээх холбоос хүчингүй эсвэл хугацаа нь дууссан байна. Шинэ холбоос авна уу.'

export default function ResetPasswordPage() {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const navigate = useNavigate()
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    const invalid = validateNewPassword(password, confirm)
    if (invalid) { setError(invalid); return }
    setLoading(true)
    setError(null)
    try {
      await api.post('/auth/reset-password', { token, password })
      setDone(true)
    } catch (err) {
      const badToken = err instanceof HttpError && [404, 410].includes(err.status)
      setError(badToken ? INVALID_LINK : authErrorMessage(err, { 400: err instanceof HttpError && err.message ? err.message : INVALID_LINK }))
    } finally {
      setLoading(false)
    }
  }

  const footer = (
    <p className="mt-5 text-center text-xs text-[var(--fg-subtle)]">
      <Link to="/forgot-password" className={linkCls}>Шинэ холбоос авах</Link>
    </p>
  )

  if (!token) {
    return (
      <AuthLayout title="Шинэ нууц үг" footer={footer}>
        <StatusBlock icon={<XCircle className="text-[var(--danger)]" />} title="Холбоос буруу байна">
          <span role="alert">Сэргээх токен олдсонгүй. И-мэйл дэх холбоосыг бүтнээр нь нээнэ үү.</span>
        </StatusBlock>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout title="Шинэ нууц үг" subtitle={done ? undefined : 'Шинэ нууц үгээ хоёр удаа оруулна уу'} footer={done ? undefined : footer}>
      {done ? (
        <div className="space-y-5">
          <StatusBlock icon={<CheckCircle2 className="text-[var(--success)]" />} title="Нууц үг шинэчлэгдлээ">
            Бүх төхөөрөмж дээрх нэвтрэлт цуцлагдсан. Шинэ нууц үгээрээ нэвтэрнэ үү.
          </StatusBlock>
          <Button size="lg" className="w-full" onClick={() => navigate('/login', { replace: true })}>Нэвтрэх</Button>
        </div>
      ) : (
        <form onSubmit={onSubmit} noValidate className="space-y-4">
          {error && <FormAlert>{error}</FormAlert>}
          <div className="space-y-2">
            <Field label="Шинэ нууц үг">
              <PasswordInput name="new-password" autoComplete="new-password" autoFocus placeholder="••••••••"
                value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <PasswordStrength password={password} />
          </div>
          <Field label="Нууц үг давтах">
            <PasswordInput name="confirm-password" autoComplete="new-password" placeholder="••••••••"
              value={confirm} onChange={(e) => setConfirm(e.target.value)} />
          </Field>
          <Button type="submit" size="lg" className="w-full" loading={loading}>Нууц үг шинэчлэх</Button>
        </form>
      )}
    </AuthLayout>
  )
}
