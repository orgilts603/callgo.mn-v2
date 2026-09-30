import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { CheckCircle2, MailWarning, XCircle } from 'lucide-react'
import { toast } from 'sonner'
import { resendVerification, useAuth } from '@/app/auth'
import { Button, Spinner } from '@/components/ui'
import { api, HttpError } from '@/lib/api'
import type { User } from '@/lib/types'
import { AuthLayout, StatusBlock } from './AuthLayout'
import { authErrorMessage } from './lib'

type State = { kind: 'verifying' } | { kind: 'success' } | { kind: 'failed'; message: string } | { kind: 'missing' }

function failMessage(err: unknown): string {
  if (err instanceof HttpError && [400, 404, 410].includes(err.status)) {
    return 'Баталгаажуулах холбоос хүчингүй эсвэл хугацаа нь дууссан байна.'
  }
  return authErrorMessage(err)
}

export default function VerifyEmailPage() {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const signedIn = useAuth((s) => !!s.token)
  const [state, setState] = useState<State>(token ? { kind: 'verifying' } : { kind: 'missing' })
  const [resending, setResending] = useState(false)
  const started = useRef<string | null>(null)
  const navigate = useNavigate()

  useEffect(() => {
    // Tokens are single use: guard against the StrictMode double effect.
    if (!token || started.current === token) return
    started.current = token
    api.post<{ user: User }>('/auth/verify-email', { token })
      .then((res) => {
        const { user, setUser } = useAuth.getState()
        if (res?.user && user?.id === res.user.id) setUser(res.user)
        setState({ kind: 'success' })
      })
      .catch((err: unknown) => setState({ kind: 'failed', message: failMessage(err) }))
  }, [token])

  const resend = async () => {
    setResending(true)
    try {
      await resendVerification()
      toast.success('Баталгаажуулах и-мэйлийг дахин илгээлээ.')
    } catch (err) {
      toast.error(authErrorMessage(err))
    } finally {
      setResending(false)
    }
  }

  const continueTo = signedIn ? '/' : '/login'
  return (
    <AuthLayout title="И-мэйл баталгаажуулалт">
      {state.kind === 'verifying' && (
        <div className="flex flex-col items-center gap-3 py-4 text-center text-[13px] text-[var(--fg-muted)]">
          <Spinner className="h-5 w-5" />
          Баталгаажуулж байна…
        </div>
      )}
      {state.kind === 'success' && (
        <div className="space-y-5">
          <StatusBlock icon={<CheckCircle2 className="text-[var(--success)]" />} title="И-мэйл баталгаажлаа">
            Таны и-мэйл хаяг амжилттай баталгаажлаа. Бүх боломжийг ашиглах боломжтой.
          </StatusBlock>
          <Button size="lg" className="w-full" onClick={() => navigate(continueTo, { replace: true })}>Үргэлжлүүлэх</Button>
        </div>
      )}
      {(state.kind === 'failed' || state.kind === 'missing') && (
        <div className="space-y-5">
          <StatusBlock
            icon={state.kind === 'failed' ? <XCircle className="text-[var(--danger)]" /> : <MailWarning className="text-[var(--warning)]" />}
            title="Баталгаажуулж чадсангүй"
          >
            <span role="alert">{state.kind === 'failed' ? state.message : 'Холбоос буруу байна: баталгаажуулах токен олдсонгүй.'}</span>
          </StatusBlock>
          <div className="flex flex-col gap-2">
            {signedIn && <Button size="lg" className="w-full" loading={resending} onClick={resend}>Дахин илгээх</Button>}
            <Button variant="secondary" size="lg" className="w-full" onClick={() => navigate(continueTo, { replace: true })}>
              {signedIn ? 'Самбар руу буцах' : 'Нэвтрэх хуудас руу'}
            </Button>
          </div>
        </div>
      )}
    </AuthLayout>
  )
}
