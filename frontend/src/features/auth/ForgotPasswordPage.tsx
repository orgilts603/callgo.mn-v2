import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeft, MailCheck } from 'lucide-react'
import { Button, Field, Input } from '@/components/ui'
import { api } from '@/lib/api'
import { AuthLayout, authErrorMessage, FormAlert, isValidEmail, linkCls, StatusBlock } from './AuthLayout'

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [sentTo, setSentTo] = useState<string | null>(null)

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!isValidEmail(email)) { setError('Зөв и-мэйл хаяг оруулна уу.'); return }
    setLoading(true)
    setError(null)
    try {
      await api.post('/auth/forgot-password', { email: email.trim() })
      setSentTo(email.trim())
    } catch (err) {
      setError(authErrorMessage(err))
    } finally {
      setLoading(false)
    }
  }

  return (
    <AuthLayout
      title="Нууц үг сэргээх"
      subtitle="Бүртгэлтэй и-мэйл хаягаа оруулна уу"
      footer={
        <p className="mt-5 text-center text-xs text-[var(--fg-subtle)]">
          <Link to="/login" className={`inline-flex items-center gap-1 ${linkCls}`}><ArrowLeft className="h-3 w-3" /> Нэвтрэх хуудас руу буцах</Link>
        </p>
      }
    >
      {sentTo ? (
        <StatusBlock icon={<MailCheck className="text-[var(--success)]" />} title="И-мэйлээ шалгана уу">
          Хэрэв <span className="font-medium text-[var(--fg)]">{sentTo}</span> хаягаар бүртгэл байгаа бол нууц үг сэргээх холбоосыг илгээлээ.
          Холбоос 1 цагийн дараа хүчингүй болно.
        </StatusBlock>
      ) : (
        <form onSubmit={onSubmit} noValidate className="space-y-4">
          {error && <FormAlert>{error}</FormAlert>}
          <Field label="И-мэйл">
            <Input type="email" name="email" autoComplete="email" autoFocus placeholder="name@company.mn" className="h-9"
              value={email} aria-invalid={!!error || undefined} onChange={(e) => setEmail(e.target.value)} />
          </Field>
          <Button type="submit" size="lg" className="w-full" loading={loading}>Сэргээх холбоос илгээх</Button>
        </form>
      )}
    </AuthLayout>
  )
}
