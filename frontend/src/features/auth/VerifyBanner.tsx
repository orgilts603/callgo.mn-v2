import { useEffect, useState } from 'react'
import { MailWarning, X } from 'lucide-react'
import { toast } from 'sonner'
import { resendVerification, useAuth } from '@/app/auth'
import { Button } from '@/components/ui'
import { cn } from '@/lib/utils'
import { authErrorMessage } from './AuthLayout'

const DISMISS_KEY = 'callgo.verifyBanner.dismissed'
export const RESEND_COOLDOWN_MS = 60_000

function readDismissed(userId: string): boolean {
  try { return sessionStorage.getItem(DISMISS_KEY) === userId } catch { return false }
}
function writeDismissed(userId: string) {
  try { sessionStorage.setItem(DISMISS_KEY, userId) } catch { /* private mode */ }
}

/** Sends the verification email again, with a cooldown so the button cannot be hammered. */
export function useResendVerification() {
  const [pending, setPending] = useState(false)
  const [sentAt, setSentAt] = useState<number | null>(null)
  useEffect(() => {
    if (sentAt === null) return
    const t = setTimeout(() => setSentAt(null), RESEND_COOLDOWN_MS)
    return () => clearTimeout(t)
  }, [sentAt])
  const resend = async () => {
    setPending(true)
    try {
      await resendVerification()
      setSentAt(Date.now())
      toast.success('Баталгаажуулах и-мэйлийг дахин илгээлээ. Ирсэн хайрцгаа шалгана уу.')
    } catch (err) {
      toast.error(authErrorMessage(err))
    } finally {
      setPending(false)
    }
  }
  return { resend, pending, sent: sentAt !== null }
}

/**
 * Dismissible notice for users whose email is not verified yet (mounted in AppShell above the page).
 * Dismissal lasts for the browser session.
 */
export function VerifyBanner({ className }: { className?: string }) {
  const user = useAuth((s) => s.user)
  const [dismissedFor, setDismissedFor] = useState<string | null>(null)
  const { resend, pending, sent } = useResendVerification()

  if (!user || user.emailVerifiedAt) return null
  if (dismissedFor === user.id || readDismissed(user.id)) return null

  const dismiss = () => { writeDismissed(user.id); setDismissedFor(user.id) }
  return (
    <div role="region" aria-label="И-мэйл баталгаажуулалт"
      className={cn('flex items-center gap-3 border-b border-[var(--warning-border)] bg-[var(--warning-soft)] px-4 py-2 text-[13px] text-[var(--warning-fg)] xl:px-6', className)}>
      <MailWarning className="h-4 w-4 shrink-0" aria-hidden />
      <p className="min-w-0 flex-1">
        <span className="font-medium">И-мэйлээ баталгаажуулна уу.</span>{' '}
        <span className="opacity-90"><span className="font-medium">{user.email}</span> хаяг руу илгээсэн холбоосыг нээнэ үү.</span>
      </p>
      <Button size="sm" variant="outline" onClick={resend} loading={pending} disabled={sent}
        className="border-[var(--warning-border)] text-[var(--warning-fg)] hover:bg-[var(--warning-soft)]">
        {sent ? 'Илгээлээ' : 'Дахин илгээх'}
      </Button>
      <button type="button" onClick={dismiss} aria-label="Хаах"
        className="rounded p-1 opacity-80 transition-opacity hover:opacity-100">
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  )
}

export default VerifyBanner
