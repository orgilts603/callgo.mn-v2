import { useState, type FormEvent } from 'react'
import { Link, Navigate, useNavigate } from 'react-router-dom'
import { ArrowRight } from 'lucide-react'
import { toast } from 'sonner'
import { useAuth } from '@/app/auth'
import { Button, Field, Input } from '@/components/ui'
import { AuthLayout, FormAlert, PasswordInput, PasswordStrength } from './AuthLayout'
import { authErrorMessage, isValidEmail, linkCls, validateNewPassword } from './lib'

type FieldKey = 'orgName' | 'name' | 'email' | 'password' | 'terms'
type Errors = Partial<Record<FieldKey, string>>

function validate(v: { orgName: string; name: string; email: string; password: string; terms: boolean }): Errors {
  const e: Errors = {}
  if (!v.orgName.trim()) e.orgName = 'Байгууллагын нэрээ оруулна уу.'
  if (!v.name.trim()) e.name = 'Нэрээ оруулна уу.'
  if (!isValidEmail(v.email)) e.email = 'Зөв и-мэйл хаяг оруулна уу.'
  const pw = validateNewPassword(v.password)
  if (pw) e.password = pw
  if (!v.terms) e.terms = 'Үйлчилгээний нөхцөлийг зөвшөөрнө үү.'
  return e
}

export default function SignupPage() {
  const token = useAuth((s) => s.token)
  const signup = useAuth((s) => s.signup)
  const navigate = useNavigate()

  const [orgName, setOrgName] = useState('')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [phone, setPhone] = useState('')
  const [terms, setTerms] = useState(false)
  const [errors, setErrors] = useState<Errors>({})
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  if (token && !loading) return <Navigate to="/" replace />

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    const v = validate({ orgName, name, email, password, terms })
    setErrors(v)
    if (Object.keys(v).length) return
    setLoading(true)
    setError(null)
    try {
      await signup({ orgName, name, email, password, phone })
      toast.success('Бүртгэл амжилттай үүслээ. И-мэйлээ баталгаажуулна уу.')
      navigate('/', { replace: true, state: { justSignedUp: true } })
    } catch (err) {
      setError(authErrorMessage(err, {
        409: 'Энэ и-мэйл хаягаар бүртгэл үүссэн байна. Нэвтэрч орно уу.',
        403: 'Шинээр бүртгүүлэх боломж одоогоор хаалттай байна.',
      }))
      setLoading(false)
    }
  }

  return (
    <AuthLayout
      wide
      title="Бүртгэл үүсгэх"
      subtitle="14 хоногийн үнэгүй туршилт. Карт шаардлагагүй."
      footer={
        <p className="mt-5 text-center text-xs text-[var(--fg-subtle)]">
          Бүртгэлтэй юу? <Link to="/login" className={linkCls}>Нэвтрэх</Link>
        </p>
      }
    >
      <form onSubmit={onSubmit} noValidate className="space-y-4">
        {error && <FormAlert>{error}</FormAlert>}
        <Field label="Байгууллагын нэр" error={errors.orgName}>
          <Input name="organization" autoComplete="organization" autoFocus placeholder="Жишээ ХХК" className="h-9"
            value={orgName} aria-invalid={!!errors.orgName || undefined} onChange={(e) => setOrgName(e.target.value)} />
        </Field>
        <Field label="Таны нэр" error={errors.name}>
          <Input name="name" autoComplete="name" placeholder="Бат Болд" className="h-9"
            value={name} aria-invalid={!!errors.name || undefined} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="И-мэйл" error={errors.email}>
          <Input type="email" name="email" autoComplete="email" placeholder="name@company.mn" className="h-9"
            value={email} aria-invalid={!!errors.email || undefined} onChange={(e) => setEmail(e.target.value)} />
        </Field>
        <div className="space-y-2">
          <Field label="Нууц үг" error={errors.password}>
            <PasswordInput name="new-password" autoComplete="new-password" placeholder="••••••••"
              value={password} aria-invalid={!!errors.password || undefined} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <PasswordStrength password={password} />
        </div>
        <Field label="Утас (заавал биш)">
          <Input type="tel" name="phone" autoComplete="tel" inputMode="tel" placeholder="+976 9911 2233" className="h-9"
            value={phone} onChange={(e) => setPhone(e.target.value)} />
        </Field>
        <div className="space-y-1">
          <label className="flex cursor-pointer items-start gap-2 text-xs text-[var(--fg-muted)]">
            <input type="checkbox" checked={terms} onChange={(e) => setTerms(e.target.checked)}
              aria-invalid={!!errors.terms || undefined}
              className="mt-0.5 h-3.5 w-3.5 shrink-0 cursor-pointer accent-[var(--accent)]" />
            <span>Үйлчилгээний нөхцөл болон нууцлалын бодлогыг зөвшөөрч байна</span>
          </label>
          {errors.terms && <span className="block text-xs text-[var(--danger)]" role="alert">{errors.terms}</span>}
        </div>
        <Button type="submit" size="lg" className="w-full" loading={loading}>
          {loading ? 'Бүртгэж байна…' : <>Бүртгүүлэх <ArrowRight /></>}
        </Button>
      </form>
    </AuthLayout>
  )
}
