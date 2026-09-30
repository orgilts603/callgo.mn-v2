// Auth store (zustand). Holds the JWT, the current user and org.
import { create } from 'zustand'
import { api, getToken, HttpError, setToken } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type { Organization, User } from '@/lib/types'

export interface LoginResponse { token: string; user: User; org: Organization }
export interface MeResponse { user: User; org: Organization }

export type AuthStatus = 'idle' | 'loading' | 'ready' | 'error'

export interface AuthState {
  token: string | null
  user: User | null
  org: Organization | null
  /** State of the last GET /auth/me. */
  status: AuthStatus
  login: (email: string, password: string) => Promise<void>
  logout: () => void
  loadMe: () => Promise<void>
}

export const useAuth = create<AuthState>((set, get) => ({
  token: getToken(),
  user: null,
  org: null,
  status: 'idle',

  login: async (email, password) => {
    const res = await api.post<LoginResponse>('/auth/login', { email: email.trim(), password })
    setToken(res.token)
    set({ token: res.token, user: res.user, org: res.org, status: 'ready' })
  },

  logout: () => {
    setToken(null)
    useLive.getState().disconnect()
    set({ token: null, user: null, org: null, status: 'idle' })
  },

  loadMe: async () => {
    if (!get().token) return
    set({ status: 'loading' })
    try {
      const res = await api.get<MeResponse>('/auth/me')
      set({ user: res.user, org: res.org, status: 'ready' })
    } catch (err) {
      if (err instanceof HttpError && err.status === 401) get().logout()
      else set({ status: 'error' })
    }
  },
}))

/** True for errors that mean the session is gone. */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof HttpError && err.status === 401
}

const roleLabels: Record<User['role'], string> = { owner: 'Эзэмшигч', admin: 'Админ', operator: 'Оператор' }
export function roleLabel(role: User['role'] | undefined): string {
  return role ? roleLabels[role] : ''
}
