import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, getToken, HttpError } from '@/lib/api'
import { useAuth } from './auth'
import { createQueryClient, handleUnauthorized } from './queryClient'
import { resetAuth, signIn, stubLive, testOrg, testUser } from './testing'

describe('auth store', () => {
  beforeEach(() => { resetAuth(); stubLive() })
  afterEach(() => vi.restoreAllMocks())

  it('loadMe fills user and org', async () => {
    signIn()
    useAuth.setState({ user: null, org: null, status: 'idle' })
    vi.spyOn(api, 'get').mockResolvedValue({ user: testUser, org: testOrg })
    await useAuth.getState().loadMe()
    expect(useAuth.getState()).toMatchObject({ user: testUser, org: testOrg, status: 'ready' })
  })

  it('loadMe logs out on 401', async () => {
    const { disconnect } = stubLive()
    signIn()
    vi.spyOn(api, 'get').mockRejectedValue(new HttpError(401, 'unauthorized', 'expired'))
    await useAuth.getState().loadMe()
    expect(useAuth.getState().token).toBeNull()
    expect(getToken()).toBeNull()
    expect(disconnect).toHaveBeenCalled()
  })

  it('loadMe keeps the session on network errors', async () => {
    signIn()
    vi.spyOn(api, 'get').mockRejectedValue(new TypeError('fetch failed'))
    await useAuth.getState().loadMe()
    expect(useAuth.getState()).toMatchObject({ token: 'test-token', status: 'error' })
  })

  it('a 401 from any query logs out', async () => {
    signIn()
    const qc = createQueryClient()
    await qc.fetchQuery({ queryKey: ['x'], queryFn: () => Promise.reject(new HttpError(401, 'unauthorized', 'no')) }).catch(() => {})
    expect(useAuth.getState().token).toBeNull()
  })

  it('ignores non-401 errors', () => {
    signIn()
    handleUnauthorized(new HttpError(500, 'internal', 'boom'))
    expect(useAuth.getState().token).toBe('test-token')
  })
})
