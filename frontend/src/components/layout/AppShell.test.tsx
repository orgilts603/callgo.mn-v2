import { act, fireEvent, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useAuth } from '@/app/auth'
import { buildRoutes } from '@/app/router'
import { renderRoutes, resetAuth, signIn, stubLive, testUser } from '@/app/testing'
import { useUI } from '@/app/ui'
import { useLive } from '@/lib/ws'
import type { Call } from '@/lib/types'
import { AppShell } from './AppShell'

const shellRoutes = [
  {
    path: '/',
    element: <AppShell />,
    children: [
      { index: true, element: <div>DASH</div> },
      { path: 'live', element: <div>LIVE PAGE</div> },
      { path: 'calls/:id', element: <div>CALL DETAIL</div> },
    ],
  },
  { path: '/login', element: <div>LOGIN</div> },
]

function fakeCall(id: string): Call {
  return {
    id, orgId: 'o1', direction: 'inbound', status: 'active', fromNumber: '+97699112233', toNumber: '+97677000000',
    roomName: `room-${id}`, startedAt: '2026-09-30T10:00:00Z', durationSec: 0, createdAt: '2026-09-30T10:00:00Z', updatedAt: '2026-09-30T10:00:00Z',
  }
}

describe('AppShell', () => {
  beforeEach(() => { resetAuth(); stubLive(); signIn(); useUI.setState({ sidebarCollapsed: false }) })
  afterEach(() => vi.restoreAllMocks())

  it('renders every nav item', () => {
    renderRoutes(shellRoutes)
    const sidebar = screen.getByRole('complementary', { name: 'Үндсэн цэс' })
    const expected: [string, string][] = [
      ['Хяналтын самбар', '/'], ['Live Desk', '/live'], ['Дуудлагын түүх', '/calls'], ['Харилцагчид', '/contacts'],
      ['Кампанит ажил', '/campaigns'], ['Lexicon', '/lexicon'], ['Тохиргоо', '/settings'],
    ]
    for (const [label, href] of expected) {
      expect(within(sidebar).getByRole('link', { name: new RegExp(label) })).toHaveAttribute('href', href)
    }
    expect(screen.getByText('DASH')).toBeInTheDocument()
  })

  it('WS pill reacts to the live store status', () => {
    renderRoutes(shellRoutes)
    const pill = () => screen.getByRole('status', { name: /Холболт/ })
    expect(pill()).toHaveTextContent('Салсан')
    act(() => useLive.setState({ status: 'connecting' }))
    expect(pill()).toHaveTextContent('Холбогдож байна')
    act(() => useLive.setState({ status: 'open' }))
    expect(pill()).toHaveTextContent('Онлайн')
    expect(pill()).toHaveAttribute('data-status', 'open')
  })

  it('shows the active calls counter from the live store', () => {
    renderRoutes(shellRoutes)
    expect(screen.getByRole('link', { name: 'Идэвхтэй дуудлага: 0' })).not.toHaveAttribute('data-active')
    act(() => useLive.setState({ activeCalls: { a: fakeCall('a'), b: fakeCall('b') } }))
    const badge = screen.getByRole('link', { name: 'Идэвхтэй дуудлага: 2' })
    expect(badge).toHaveAttribute('data-active', 'true')
  })

  it('marks the current route active and builds a breadcrumb', () => {
    renderRoutes(shellRoutes, { path: '/calls/abc' })
    expect(screen.getByText('CALL DETAIL')).toBeInTheDocument()
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' })
    expect(within(crumbs).getByRole('link', { name: 'Дуудлагын түүх' })).toHaveAttribute('href', '/calls')
    expect(within(crumbs).getByText('Дэлгэрэнгүй')).toHaveAttribute('aria-current', 'page')
    const sidebar = screen.getByRole('complementary', { name: 'Үндсэн цэс' })
    expect(within(sidebar).getByRole('link', { name: /Дуудлагын түүх/ })).toHaveAttribute('aria-current', 'page')
  })

  it('collapses the sidebar and remembers it', () => {
    renderRoutes(shellRoutes)
    fireEvent.click(screen.getByRole('button', { name: 'Цэсийг хураах' }))
    expect(screen.getByRole('complementary', { name: 'Үндсэн цэс' })).toHaveAttribute('data-collapsed', 'true')
    expect(localStorage.getItem('callgo.sidebar.collapsed')).toBe('1')
    fireEvent.click(screen.getByRole('button', { name: 'Цэсийг дэлгэх' }))
    expect(localStorage.getItem('callgo.sidebar.collapsed')).toBe('0')
  })

  it('"[" toggles the sidebar and "g l" navigates', async () => {
    renderRoutes(shellRoutes)
    fireEvent.keyDown(window, { key: '[' })
    expect(useUI.getState().sidebarCollapsed).toBe(true)
    fireEvent.keyDown(window, { key: 'g' })
    fireEvent.keyDown(window, { key: 'l' })
    expect(await screen.findByText('LIVE PAGE')).toBeInTheDocument()
  })

  it('user menu shows the user and logs out', async () => {
    const { disconnect } = stubLive()
    renderRoutes(shellRoutes)
    fireEvent.click(screen.getByRole('button', { name: 'Хэрэглэгчийн цэс' }))
    const menu = screen.getByRole('menu')
    expect(within(menu).getByText(testUser.email)).toBeInTheDocument()
    expect(within(menu).getByText('Админ')).toBeInTheDocument()
    fireEvent.click(within(menu).getByRole('menuitem', { name: /Гарах/ }))
    expect(useAuth.getState().token).toBeNull()
    expect(localStorage.getItem('callgo.token')).toBeNull()
    expect(disconnect).toHaveBeenCalled()
  })
})

describe('app routes', () => {
  beforeEach(() => { resetAuth(); stubLive() })

  it('redirects to /login without a token', async () => {
    const { router } = renderRoutes(buildRoutes([]), { path: '/calls' })
    await screen.findByRole('button', { name: /Нэвтрэх/ })
    expect(router.state.location.pathname).toBe('/login')
  })

  it('renders feature routes inside the shell and a 404 for unknown paths', async () => {
    signIn()
    const routes = buildRoutes([{ path: 'lexicon', element: <div>LEXICON FEATURE</div>, handle: { crumb: 'Толь бичиг' } }])
    const { router } = renderRoutes(routes, { path: '/lexicon' })
    expect(await screen.findByText('LEXICON FEATURE')).toBeInTheDocument()
    expect(screen.getByRole('complementary', { name: 'Үндсэн цэс' })).toBeInTheDocument()
    expect(within(screen.getByRole('navigation', { name: 'Breadcrumb' })).getByText('Толь бичиг')).toBeInTheDocument()
    await act(() => router.navigate('/nope'))
    expect(await screen.findByText('Хуудас олдсонгүй', { selector: 'div' })).toBeInTheDocument()
  })

  it('keeps the shell when a page crashes', async () => {
    signIn()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    function Boom(): never { throw new Error('kaboom') }
    const { router } = renderRoutes(buildRoutes([{ path: 'boom', element: <Boom /> }, { path: 'ok', element: <div>OK PAGE</div> }]), { path: '/boom' })
    expect(await screen.findByText('Уучлаарай, алдаа гарлаа')).toBeInTheDocument()
    expect(screen.getByText('kaboom')).toBeInTheDocument()
    expect(screen.getByRole('complementary', { name: 'Үндсэн цэс' })).toBeInTheDocument()
    await act(() => router.navigate('/ok'))
    expect(await screen.findByText('OK PAGE')).toBeInTheDocument()
  })
})
