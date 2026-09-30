import { MutationCache, QueryCache, QueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { isUnauthorized, useAuth } from './auth'

/** Any 401 (query or mutation) ends the session; RequireAuth then redirects to /login. */
export function handleUnauthorized(err: unknown) {
  if (!isUnauthorized(err)) return
  const { token, logout } = useAuth.getState()
  if (!token) return
  logout()
  toast.error('Нэвтрэлтийн хугацаа дууссан. Дахин нэвтэрнэ үү.', { id: 'session-expired' })
}

export function createQueryClient() {
  return new QueryClient({
    queryCache: new QueryCache({ onError: handleUnauthorized }),
    mutationCache: new MutationCache({ onError: handleUnauthorized }),
    defaultOptions: {
      queries: {
        staleTime: 10_000,
        retry: (count, err) => !isUnauthorized(err) && count < 1,
        refetchOnWindowFocus: true,
      },
      mutations: { retry: false },
    },
  })
}
