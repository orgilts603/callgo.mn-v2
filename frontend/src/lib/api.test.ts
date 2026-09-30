import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, getRefreshToken, getToken, HttpError, setRefreshToken, setToken } from './api'

function json(status: number, body: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
const unauthorized = () => json(401, { error: { code: 'unauthorized', message: 'token expired' } })

type Call = { url: string; auth?: string; body?: string }
function calls(fetchMock: ReturnType<typeof vi.fn>): Call[] {
  return fetchMock.mock.calls.map(([url, init]) => ({
    url: String(url),
    auth: (init?.headers as Record<string, string> | undefined)?.Authorization,
    body: init?.body as string | undefined,
  }))
}

describe('api client refresh tokens', () => {
  let fetchMock: ReturnType<typeof vi.fn>
  beforeEach(() => {
    localStorage.clear()
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => vi.unstubAllGlobals())

  it('stores and clears the refresh token under callgo.refresh', () => {
    setRefreshToken('r1')
    expect(localStorage.getItem('callgo.refresh')).toBe('r1')
    expect(getRefreshToken()).toBe('r1')
    setRefreshToken(null)
    expect(getRefreshToken()).toBeNull()
  })

  it('refreshes on 401, stores the rotated pair and retries the request once', async () => {
    setToken('old'); setRefreshToken('r1')
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url === '/api/auth/refresh') return json(200, { token: 'new', refreshToken: 'r2' })
      const auth = (init?.headers as Record<string, string>).Authorization
      return auth === 'Bearer new' ? json(200, { items: [1] }) : unauthorized()
    })

    await expect(api.get('/calls', { limit: 5 })).resolves.toEqual({ items: [1] })

    const c = calls(fetchMock)
    expect(c.map((x) => x.url)).toEqual(['/api/calls?limit=5', '/api/auth/refresh', '/api/calls?limit=5'])
    expect(JSON.parse(c[1].body ?? '{}')).toEqual({ refreshToken: 'r1' })
    expect(c[2].auth).toBe('Bearer new')
    expect(getToken()).toBe('new')
    expect(getRefreshToken()).toBe('r2')
  })

  it('shares one refresh between concurrent 401s (single flight)', async () => {
    setToken('old'); setRefreshToken('r1')
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => { release = r })
    fetchMock.mockImplementation(async (url: string, init?: RequestInit) => {
      if (url === '/api/auth/refresh') { await gate; return json(200, { token: 'new', refreshToken: 'r2' }) }
      const auth = (init?.headers as Record<string, string>).Authorization
      return auth === 'Bearer new' ? json(200, { url }) : unauthorized()
    })

    const all = Promise.all([api.get('/a'), api.get('/b'), api.post('/c', { x: 1 })])
    await vi.waitFor(() => expect(fetchMock.mock.calls.filter(([u]) => u === '/api/auth/refresh')).toHaveLength(1))
    release()
    await expect(all).resolves.toEqual([{ url: '/api/a' }, { url: '/api/b' }, { url: '/api/c' }])
    expect(fetchMock.mock.calls.filter(([u]) => u === '/api/auth/refresh')).toHaveLength(1)
    expect(fetchMock).toHaveBeenCalledTimes(7)
  })

  it('does not retry more than once when the retried request is still 401', async () => {
    setToken('old'); setRefreshToken('r1')
    fetchMock.mockImplementation(async (url: string) =>
      url === '/api/auth/refresh' ? json(200, { token: 'new', refreshToken: 'r2' }) : unauthorized())
    await expect(api.get('/calls')).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(getToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
  })

  it('clears both tokens and throws 401 when the refresh fails', async () => {
    setToken('old'); setRefreshToken('r1')
    fetchMock.mockImplementation(async (url: string) =>
      url === '/api/auth/refresh' ? json(401, { error: { code: 'unauthorized', message: 'revoked' } }) : unauthorized())
    const err = await api.get('/calls').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(HttpError)
    expect(err).toMatchObject({ status: 401, code: 'unauthorized' })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(getToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
  })

  it('clears both tokens when the refresh request cannot reach the server', async () => {
    setToken('old'); setRefreshToken('r1')
    fetchMock.mockImplementation(async (url: string) => {
      if (url === '/api/auth/refresh') throw new TypeError('network down')
      return unauthorized()
    })
    await expect(api.get('/calls')).rejects.toMatchObject({ status: 401 })
    expect(getToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
  })

  it('never refreshes for credential endpoints (login 401 = wrong password)', async () => {
    setRefreshToken('r1')
    fetchMock.mockResolvedValue(unauthorized())
    await expect(api.post('/auth/login', { email: 'a', password: 'b' })).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('keeps the legacy behaviour without a refresh token: 401 clears the token, no refresh call', async () => {
    setToken('old')
    fetchMock.mockResolvedValue(unauthorized())
    await expect(api.get('/calls')).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(getToken()).toBeNull()
  })

  it('retries without refreshing when another request already rotated the token', async () => {
    setToken('old'); setRefreshToken('r1')
    fetchMock.mockImplementation(async (_url: string, init?: RequestInit) => {
      const auth = (init?.headers as Record<string, string>).Authorization
      if (auth === 'Bearer old') { setToken('rotated'); return unauthorized() }
      return json(200, { ok: true })
    })
    await expect(api.get('/calls')).resolves.toEqual({ ok: true })
    expect(fetchMock.mock.calls.map(([u]) => u)).toEqual(['/api/calls', '/api/calls'])
  })

  it('passes non-401 errors through untouched', async () => {
    setToken('t'); setRefreshToken('r1')
    fetchMock.mockResolvedValue(json(402, { error: { code: 'payment_required', message: 'pay' } }))
    await expect(api.get('/calls')).rejects.toMatchObject({ status: 402, code: 'payment_required' })
    expect(getToken()).toBe('t')
    expect(getRefreshToken()).toBe('r1')
  })
})
