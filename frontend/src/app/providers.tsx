import { useEffect, useState, type ReactNode } from 'react'
import { QueryClientProvider, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { Toaster } from 'sonner'
import { useLive } from '@/lib/ws'
import { useAuth } from './auth'
import { createQueryClient, handleUnauthorized } from './queryClient'
import { applyTheme, useUI } from './ui'

/**
 * Keeps session side effects in one place:
 *  - token present → load /auth/me (once) and connect the live socket;
 *  - token cleared → close the socket and drop every cached query;
 *  - a 401 escaping outside react-query (unhandled rejection) also logs out.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const token = useAuth((s) => s.token)
  const qc = useQueryClient()

  useEffect(() => {
    if (token) {
      const { user, status, loadMe } = useAuth.getState()
      if (!user && status !== 'loading') void loadMe()
      useLive.getState().connect()
    } else {
      useLive.getState().disconnect()
      qc.clear()
    }
  }, [token, qc])

  useEffect(() => {
    const onRejection = (e: PromiseRejectionEvent) => handleUnauthorized(e.reason)
    window.addEventListener('unhandledrejection', onRejection)
    return () => window.removeEventListener('unhandledrejection', onRejection)
  }, [])

  return <>{children}</>
}

function ThemedToaster() {
  const theme = useUI((s) => s.theme)
  useEffect(() => applyTheme(theme), [theme])
  return (
    <Toaster
      theme={theme}
      position="bottom-right"
      closeButton
      toastOptions={{
        style: {
          background: 'var(--surface-overlay)',
          border: '1px solid var(--border)',
          color: 'var(--fg)',
          fontSize: '13px',
          boxShadow: 'var(--shadow-lg)',
        },
      }}
    />
  )
}

export function Providers({ children, client }: { children: ReactNode; client?: QueryClient }) {
  const [qc] = useState(() => client ?? createQueryClient())
  return (
    <QueryClientProvider client={qc}>
      <AuthProvider>{children}</AuthProvider>
      <ThemedToaster />
    </QueryClientProvider>
  )
}
