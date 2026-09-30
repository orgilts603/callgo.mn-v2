import { Fragment } from 'react'
import { Link, useLocation, useMatches } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import { buildCrumbs } from './crumbs'

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
