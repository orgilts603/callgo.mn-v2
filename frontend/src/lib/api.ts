// Thin typed fetch client. FROZEN CONTRACT — features import from here.
import type { ApiError } from './types'

const TOKEN_KEY = 'callgo.token'
const REFRESH_KEY = 'callgo.refresh'

export function getToken(): string | null {
  try { return localStorage.getItem(TOKEN_KEY) } catch { return null }
}
export function setToken(token: string | null) {
  try { if (token) localStorage.setItem(TOKEN_KEY, token); else localStorage.removeItem(TOKEN_KEY) } catch { /* ignore */ }
}
export function getRefreshToken(): string | null {
  try { return localStorage.getItem(REFRESH_KEY) } catch { return null }
}
export function setRefreshToken(token: string | null) {
  try { if (token) localStorage.setItem(REFRESH_KEY, token); else localStorage.removeItem(REFRESH_KEY) } catch { /* ignore */ }
}

export class HttpError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

type Query = Record<string, string | number | boolean | undefined | null>

function qs(q?: Query): string {
  if (!q) return ''
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== null && v !== '') p.set(k, String(v))
  const s = p.toString()
  return s ? `?${s}` : ''
}

/**
 * Auth endpoints that authenticate by credentials / one-time tokens. A 401 from them means
 * "wrong credentials", never "access token expired", so they must not trigger a refresh.
 * (`/auth/me`, `/auth/sessions`, `/auth/resend-verification` DO refresh.)
 */
const NO_REFRESH_PATHS = new Set([
  '/auth/login', '/auth/register', '/auth/signup', '/auth/refresh', '/auth/logout', '/auth/verify-email',
  '/auth/forgot-password', '/auth/reset-password', '/auth/accept-invitation', '/auth/change-password',
])

let refreshing: Promise<boolean> | null = null

/**
 * Exchanges the stored refresh token for a new pair (POST /api/auth/refresh). Single-flight:
 * concurrent 401s share one request, so a rotated refresh token is never spent twice.
 * Resolves true when a new access token was stored.
 */
export function refreshAccessToken(): Promise<boolean> {
  if (refreshing) return refreshing
  const refreshToken = getRefreshToken()
  if (!refreshToken) return Promise.resolve(false)
  refreshing = (async () => {
    try {
      const res = await fetch('/api/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refreshToken }),
      })
      if (!res.ok) return false
      const data = (await res.json().catch(() => null)) as { token?: string; refreshToken?: string } | null
      if (!data?.token) return false
      setToken(data.token)
      setRefreshToken(data.refreshToken || refreshToken)
      return true
    } catch {
      return false
    }
  })().finally(() => { refreshing = null })
  return refreshing
}

async function request<T>(method: string, path: string, body?: unknown, query?: Query, retried = false): Promise<T> {
  const headers: Record<string, string> = {}
  const token = getToken()
  if (token) headers.Authorization = `Bearer ${token}`
  let payload: BodyInit | undefined
  if (body instanceof FormData) payload = body
  else if (body !== undefined) { headers['Content-Type'] = 'application/json'; payload = JSON.stringify(body) }
  const res = await fetch(`/api${path}${qs(query)}`, { method, headers, body: payload })
  if (res.status === 204) return undefined as T
  if (res.status === 401 && !retried && !NO_REFRESH_PATHS.has(path)) {
    // Another request may already have refreshed while this one was in flight.
    const current = getToken()
    if (current && current !== token) return request<T>(method, path, body, query, true)
    if (getRefreshToken() && (await refreshAccessToken())) return request<T>(method, path, body, query, true)
  }
  const text = await res.text()
  let data: unknown = null
  try { data = text ? JSON.parse(text) : null } catch { data = null }
  if (!res.ok) {
    const err = (data as ApiError | null)?.error
    if (res.status === 401) { setToken(null); setRefreshToken(null) }
    throw new HttpError(res.status, err?.code ?? 'http_error', err?.message ?? res.statusText)
  }
  return data as T
}

export const api = {
  get: <T>(path: string, query?: Query) => request<T>('GET', path, undefined, query),
  post: <T>(path: string, body?: unknown, query?: Query) => request<T>('POST', path, body, query),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  delete: <T = void>(path: string) => request<T>('DELETE', path),
}
