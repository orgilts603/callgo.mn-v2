import { useCallback } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { PageHeader } from '@/components/ui'
import { CallDrawer } from './CallDrawer'

/** Deep-link page `/calls/:id`: shows the call drawer; closing goes back (or to the Live Desk). */
export function CallPage() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const location = useLocation()
  const close = useCallback(() => {
    // `default` key means this is the first entry in the history stack (opened from a link).
    if (location.key === 'default') navigate('/live', { replace: true })
    else navigate(-1)
  }, [location.key, navigate])

  return (
    <>
      <PageHeader title="Дуудлага" description="Дуудлагын дэлгэрэнгүй"
        actions={<Link to="/live" className="inline-flex items-center gap-1 text-sm text-[var(--fg-muted)] hover:text-[var(--fg)]"><ArrowLeft className="h-4 w-4" /> Live Desk</Link>} />
      <CallDrawer callId={id ?? null} onClose={close} />
    </>
  )
}

export default CallPage
