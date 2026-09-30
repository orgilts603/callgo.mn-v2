// Auth store (zustand). Holds the JWT, the current user, org and subscription.
import { create } from 'zustand'
import { api, getRefreshToken, getToken, HttpError, setRefreshToken, setToken } from '@/lib/api'
import { useLive } from '@/lib/ws'
import type { AuthResponse, Organization, Subscription, User } from '@/lib/types'

/** Login / signup / accept-invitation response. `refreshToken` is absent on legacy (24h JWT) servers. */
export interface LoginResponse { token: string; refreshToken?: string; user: User; org: Organization; subscription?: Subscription | null }
export interface MeResponse { user: User; org: Organization; subscription?: Subscription | null }

export interface SignupBody { orgName: string; name: string; email: string; password: string; phone?: string }
export interface AcceptInvitationBody { token: string; name: string; password: string }

export type AuthStatus = 'idle' | 'loading' | 'ready' | 'error'

export interface AuthState {
  token: string | null
  user: User | null
  org: Organization | null
  subscription: Subscription | null
  /** State of the last GET /auth/me. */
  status: AuthStatus
  login: (email: string, password: string) => Promise<void>
  signup: (body: SignupBody) => Promise<void>
  acceptInvitation: (body: AcceptInvitationBody) => Promise<void>
  /** Clears the session locally; revokes the refresh token server-side (best effort). */
  logout: () => void
  loadMe: () => Promise<void>
  setUser: (user: User) => void
  setOrg: (org: Organization) => void
}

export const useAuth = create<AuthState>((set, get) => {
  const startSession = (res: LoginResponse | AuthResponse) => {
    setToken(res.token)
    setRefreshToken(res.refreshToken || null)
    set({ token: res.token, user: res.user, org: res.org, subscription: res.subscription ?? null, status: 'ready' })
  }
  return {
    token: getToken(),
    user: null,
    org: null,
    subscription: null,
    status: 'idle',

    login: async (email, password) => {
      startSession(await api.post<LoginResponse>('/auth/login', { email: email.trim(), password }))
    },

    signup: async (body) => {
      const payload: SignupBody = {
        orgName: body.orgName.trim(), name: body.name.trim(), email: body.email.trim(), password: body.password,
      }
      if (body.phone?.trim()) payload.phone = body.phone.trim()
      startSession(await api.post<AuthResponse>('/auth/signup', payload))
    },

    acceptInvitation: async ({ token, name, password }) => {
      const res = await api.post<AuthResponse>('/auth/accept-invitation', { token, name: name.trim(), password })
      // Accepting while signed in elsewhere switches accounts: drop the old socket first.
      if (get().token) useLive.getState().disconnect()
      startSession(res)
    },

    logout: () => {
      const refreshToken = getRefreshToken()
      // Fire before clearing: the request picks up the current access token synchronously.
      if (refreshToken) void api.post('/auth/logout', { refreshToken }).catch(() => { /* best effort */ })
      setToken(null)
      setRefreshToken(null)
      useLive.getState().disconnect()
      set({ token: null, user: null, org: null, subscription: null, status: 'idle' })
    },

    loadMe: async () => {
      if (!get().token) return
      set({ status: 'loading' })
      try {
        const res = await api.get<MeResponse>('/auth/me')
        set({ user: res.user, org: res.org, status: 'ready', ...(res.subscription !== undefined ? { subscription: res.subscription } : {}) })
      } catch (err) {
        if (err instanceof HttpError && err.status === 401) get().logout()
        else set({ status: 'error' })
      }
    },

    setUser: (user) => set({ user }),
    setOrg: (org) => set({ org }),
  }
})

/** POST /auth/resend-verification (authenticated). */
export function resendVerification(): Promise<void> {
  return api.post<void>('/auth/resend-verification')
}

/** True for errors that mean the session is gone. */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof HttpError && err.status === 401
}

const roleLabels: Record<User['role'], string> = { owner: 'Эзэмшигч', admin: 'Админ', operator: 'Оператор' }
export function roleLabel(role: User['role'] | undefined): string {
  return role ? roleLabels[role] : ''
}
