import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { Spinner } from '@/components/ui'
import { LogoMark } from '@/components/layout/Logo'
import { useAuth } from './auth'

export function SplashScreen() {
  return (
    <div className="flex h-screen flex-col items-center justify-center gap-4 bg-[var(--surface-0)]" aria-busy="true">
      <LogoMark className="h-9 w-9" />
      <Spinner className="h-4 w-4" />
    </div>
  )
}

/** Redirects to /login without a token; waits for /auth/me before rendering the shell. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const token = useAuth((s) => s.token)
  const user = useAuth((s) => s.user)
  const status = useAuth((s) => s.status)
  const location = useLocation()
  if (!token) return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />
  if (!user && (status === 'idle' || status === 'loading')) return <SplashScreen />
  return <>{children}</>
}
