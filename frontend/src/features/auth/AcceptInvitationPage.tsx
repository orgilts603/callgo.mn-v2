import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { ArrowRight, XCircle } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { useAuth } from '@/app/auth'
import { Button, Field, Input } from '@/components/ui'
import { HttpError } from '@/lib/api'
import { AuthLayout, FormAlert, PasswordInput, PasswordStrength, StatusBlock } from './AuthLayout'
import { authErrorMessage, linkCls, validateNewPassword } from './lib'

const INVALID_LINK = 'Урилгын холбоос хүчингүй эсвэл хугацаа нь дууссан байна. Админаасаа дахин урилга илгээхийг хүснэ үү.'

export default function AcceptInvitationPage() {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const acceptInvitation = useAuth((s) => s.acceptInvitation)
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) { setError('Нэрээ оруулна уу.'); return }
    const invalid = validateNewPassword(password, confirm)
    if (invalid) { setError(invalid); return }
    setLoading(true)
    setError(null)
    try {
      // Drop whatever another session (maybe another org) left in the cache.
      qc.clear()
      await acceptInvitation({ token, name, password })
      toast.success('Тавтай морил! Урилгыг амжилттай хүлээн авлаа.')
      navigate('/', { replace: true })
    } catch (err) {
      if (err instanceof HttpError && [400, 404, 410].includes(err.status)) setError(INVALID_LINK)
      else setError(authErrorMessage(err, { 409: 'Энэ урилгыг аль хэдийн хүлээн авсан байна. Нэвтэрч орно уу.' }))
      setLoading(false)
    }
  }

  const footer = (
    <p className="mt-5 text-center text-xs text-[var(--fg-subtle)]">
      Бүртгэлтэй юу? <Link to="/login" className={linkCls}>Нэвтрэх</Link>
    </p>
  )

  if (!token) {
    return (
      <AuthLayout title="Урилга хүлээн авах" footer={footer}>
        <StatusBlock icon={<XCircle className="text-[var(--danger)]" />} title="Холбоос буруу байна">
          <span role="alert">Урилгын токен олдсонгүй. И-мэйл дэх холбоосыг бүтнээр нь нээнэ үү.</span>
        </StatusBlock>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout title="Урилга хүлээн авах" subtitle="Багтаа нэгдэхийн тулд нэр, нууц үгээ тохируулна уу" footer={footer}>
      <form onSubmit={onSubmit} noValidate className="space-y-4">
        {error && <FormAlert>{error}</FormAlert>}
        <Field label="Таны нэр">
          <Input name="name" autoComplete="name" autoFocus placeholder="Бат Болд" className="h-9"
            value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <div className="space-y-2">
          <Field label="Нууц үг">
            <PasswordInput name="new-password" autoComplete="new-password" placeholder="••••••••"
              value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <PasswordStrength password={password} />
        </div>
        <Field label="Нууц үг давтах">
          <PasswordInput name="confirm-password" autoComplete="new-password" placeholder="••••••••"
            value={confirm} onChange={(e) => setConfirm(e.target.value)} />
        </Field>
        <Button type="submit" size="lg" className="w-full" loading={loading}>
          {loading ? 'Нэгдэж байна…' : <>Нэгдэх <ArrowRight /></>}
        </Button>
      </form>
    </AuthLayout>
  )
}
