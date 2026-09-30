import { useCallback, useRef } from 'react'
import { Outlet, useLocation, useMatches } from 'react-router-dom'
import { ErrorBoundary } from './ErrorBoundary'
import { useShellHotkeys } from './hotkeys'
import { PageContainer } from './PageContainer'
import { getHandle } from './route-handle'
import { Sidebar } from './Sidebar'
import { Topbar } from './Topbar'
import { VerifyBanner } from '@/features/auth/VerifyBanner'
import { QuotaBanner } from '@/features/billing/QuotaBanner'

/** Authenticated layout: sidebar + top bar + routed page. */
export function AppShell() {
  const { pathname } = useLocation()
  const matches = useMatches()
  const fullWidth = matches.some((m) => getHandle(m)?.fullWidth)
  const searchRef = useRef<HTMLInputElement>(null)
  const focusSearch = useCallback(() => { searchRef.current?.focus(); searchRef.current?.select() }, [])
  useShellHotkeys(focusSearch)

  return (
    <div className="flex min-h-screen min-w-[1024px] bg-[var(--surface-0)] text-[var(--fg)]">
      <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-[60] focus:rounded-md focus:bg-[var(--accent)] focus:px-3 focus:py-1.5 focus:text-xs focus:text-[var(--fg-on-accent)]">
        Үндсэн агуулга руу шилжих
      </a>
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar searchRef={searchRef} />
        <VerifyBanner />
        <QuotaBanner />
        <main id="main" tabIndex={-1} className="min-h-0 flex-1 focus:outline-none">
          <ErrorBoundary resetKey={pathname}>
            <PageContainer fluid={fullWidth}>
              <Outlet />
            </PageContainer>
          </ErrorBoundary>
        </main>
      </div>
    </div>
  )
}
