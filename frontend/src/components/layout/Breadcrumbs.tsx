import { Fragment } from 'react'
import { Link, useLocation, useMatches, type UIMatch } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import { navItemFor } from './nav'
import { getHandle } from './route-handle'

export interface Crumb { label: string; to?: string }

function segmentLabel(seg: string): string {
  if (seg === 'new') return 'Шинэ'
  if (seg === 'edit') return 'Засах'
  return 'Дэлгэрэнгүй'
}

/** Route `handle.crumb`s win; otherwise derive from the nav registry + path depth. */
export function buildCrumbs(pathname: string, matches: UIMatch[]): Crumb[] {
  const fromHandles: Crumb[] = []
  for (const m of matches) {
    const c = getHandle(m)?.crumb
    if (!c) continue
    fromHandles.push({ label: typeof c === 'function' ? c(m) : c, to: m.pathname })
  }
  const nav = navItemFor(pathname)
  if (fromHandles.length > 0) {
    if (nav && fromHandles[0].label !== nav.label && nav.to !== '/') fromHandles.unshift({ label: nav.label, to: nav.to })
    return fromHandles
  }
  if (!nav) return [{ label: 'Хуудас олдсонгүй' }]
  const crumbs: Crumb[] = [{ label: nav.label, to: nav.to }]
  const rest = pathname.slice(nav.to.length).split('/').filter(Boolean)
  if (rest.length > 0) crumbs.push({ label: segmentLabel(rest[rest.length - 1]) })
  return crumbs
}

export function Breadcrumbs({ className }: { className?: string }) {
  const { pathname } = useLocation()
  const matches = useMatches()
  const crumbs = buildCrumbs(pathname, matches)
  return (
    <nav aria-label="Breadcrumb" className={cn('flex min-w-0 items-center gap-1.5 text-[13px]', className)}>
      {crumbs.map((c, i) => {
        const last = i === crumbs.length - 1
        return (
          <Fragment key={`${c.label}-${i}`}>
            {i > 0 && <ChevronRight className="h-3.5 w-3.5 shrink-0 text-[var(--fg-subtle)]" aria-hidden />}
            {last || !c.to ? (
              <span aria-current={last ? 'page' : undefined} className={cn('truncate', last ? 'font-medium text-[var(--fg)]' : 'text-[var(--fg-muted)]')}>{c.label}</span>
            ) : (
              <Link to={c.to} className="truncate text-[var(--fg-muted)] transition-colors hover:text-[var(--fg)]">{c.label}</Link>
            )}
          </Fragment>
        )
      })}
    </nav>
  )
}
