// Test helpers for the app shell and features (not bundled: only imported by *.test.tsx).
import type { ReactElement } from 'react'
import { render } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createMemoryRouter, RouterProvider, type RouteObject } from 'react-router-dom'
import { vi } from 'vitest'
import { useLive } from '@/lib/ws'
import type { Organization, User } from '@/lib/types'
import { useAuth } from './auth'

export const testUser: User = {
  id: 'u1', orgId: 'o1', email: 'admin@callgo.mn', name: 'Бат Болд', role: 'admin', createdAt: '2026-01-01T00:00:00Z', status: 'active', isPlatformAdmin: false, updatedAt: '2026-01-01T00:00:00Z',
}
export const testOrg: Organization = { id: 'o1', name: 'CallGo Demo', slug: 'demo', createdAt: '2026-01-01T00:00:00Z', planCode: 'trial', status: 'active', timezone: 'Asia/Ulaanbaatar', updatedAt: '2026-01-01T00:00:00Z' }

export function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } } })
}

/** Stubs the socket so no real WebSocket is opened, and resets live state. */
export function stubLive(overrides: Partial<ReturnType<typeof useLive.getState>> = {}) {
  const connect = vi.fn()
  const disconnect = vi.fn()
  useLive.setState({ status: 'closed', activeCalls: {}, lastEvent: null, connect, disconnect, ...overrides })
  return { connect, disconnect }
}

export function signIn(token = 'test-token') {
  localStorage.setItem('callgo.token', token)
  useAuth.setState({ token, user: testUser, org: testOrg, status: 'ready' })
}

export function resetAuth() {
  localStorage.clear()
  useAuth.setState({ token: null, user: null, org: null, subscription: null, status: 'idle' })
}

export function renderRoutes(routes: RouteObject[], { path = '/', client = makeQueryClient() }: { path?: string; client?: QueryClient } = {}) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  const utils = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  return { ...utils, router, client }
}

export function renderPage(ui: ReactElement, opts: { path?: string; client?: QueryClient } = {}) {
  return renderRoutes([{ path: '*', element: ui }], opts)
}

/** recharts' ResponsiveContainer needs ResizeObserver + a non-zero size in jsdom. */
export function installChartDom() {
  class RO {
    private cb: ResizeObserverCallback
    constructor(cb: ResizeObserverCallback) { this.cb = cb }
    observe(target: Element) {
      this.cb([{ target, contentRect: { width: 800, height: 260 } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver)
    }
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal('ResizeObserver', RO)
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(800)
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(260)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: 800, bottom: 260, width: 800, height: 260, toJSON: () => ({}),
  } as DOMRect)
}
