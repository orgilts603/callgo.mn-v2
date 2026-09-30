import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, getRefreshToken, getToken } from '@/lib/api'
import type { Subscription } from '@/lib/types'
import { useAuth } from './auth'
import { resetAuth, signIn, stubLive, testOrg, testUser } from './testing'

const sub: Subscription = {
  id: 's1', orgId: 'o1', planCode: 'trial', status: 'trialing', currentPeriodStart: '2026-09-01T00:00:00Z',
  currentPeriodEnd: '2026-09-15T00:00:00Z', createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z',
}
const authRes = { token: 'jwt', refreshToken: 'ref', user: testUser, org: testOrg, subscription: sub }

describe('auth store sessions', () => {
  beforeEach(() => { resetAuth(); useAuth.setState({ subscription: null }); stubLive() })
  afterEach(() => vi.restoreAllMocks())

  it('login stores access + refresh tokens and the subscription', async () => {
    vi.spyOn(api, 'post').mockResolvedValue(authRes)
    await useAuth.getState().login(' a@b.mn ', 'pw')
    expect(getToken()).toBe('jwt')
    expect(getRefreshToken()).toBe('ref')
    expect(useAuth.getState()).toMatchObject({ token: 'jwt', user: testUser, org: testOrg, subscription: sub, status: 'ready' })
  })

  it('signup posts the trimmed body (phone omitted when empty) and starts a session', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue(authRes)
    await useAuth.getState().signup({ orgName: ' Acme ', name: ' Bat ', email: ' bat@acme.mn ', password: 'secret123', phone: '  ' })
    expect(post).toHaveBeenCalledWith('/auth/signup', { orgName: 'Acme', name: 'Bat', email: 'bat@acme.mn', password: 'secret123' })
    expect(getRefreshToken()).toBe('ref')
    expect(useAuth.getState().token).toBe('jwt')
  })

  it('signup sends the phone when given', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue(authRes)
    await useAuth.getState().signup({ orgName: 'A', name: 'B', email: 'c@d.mn', password: 'secret123', phone: '+97699112233' })
    expect(post.mock.calls[0][1]).toMatchObject({ phone: '+97699112233' })
  })

  it('acceptInvitation stores both tokens and replaces an existing session', async () => {
    const { disconnect } = stubLive()
    signIn('previous')
    const post = vi.spyOn(api, 'post').mockResolvedValue({ ...authRes, subscription: undefined })
    await useAuth.getState().acceptInvitation({ token: 'inv', name: ' Дорж ', password: 'secret123' })
    expect(post).toHaveBeenCalledWith('/auth/accept-invitation', { token: 'inv', name: 'Дорж', password: 'secret123' })
    expect(disconnect).toHaveBeenCalled()
    expect(getToken()).toBe('jwt')
    expect(getRefreshToken()).toBe('ref')
  })

  it('logout revokes the refresh token server-side and clears everything', () => {
    const { disconnect } = stubLive()
    signIn()
    localStorage.setItem('callgo.refresh', 'ref-1')
    const post = vi.spyOn(api, 'post').mockResolvedValue(undefined)
    useAuth.getState().logout()
    expect(post).toHaveBeenCalledWith('/auth/logout', { refreshToken: 'ref-1' })
    expect(getToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
    expect(disconnect).toHaveBeenCalled()
    expect(useAuth.getState()).toMatchObject({ token: null, user: null, org: null, subscription: null })
  })

  it('logout is best effort: a failing /auth/logout does not throw', async () => {
    signIn()
    localStorage.setItem('callgo.refresh', 'ref-1')
    vi.spyOn(api, 'post').mockRejectedValue(new TypeError('offline'))
    expect(() => useAuth.getState().logout()).not.toThrow()
    await Promise.resolve()
    expect(useAuth.getState().token).toBeNull()
  })

  it('logout without a refresh token makes no request', () => {
    signIn()
    const post = vi.spyOn(api, 'post')
    useAuth.getState().logout()
    expect(post).not.toHaveBeenCalled()
  })
})
