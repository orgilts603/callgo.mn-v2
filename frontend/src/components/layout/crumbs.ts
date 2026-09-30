import type { UIMatch } from 'react-router-dom'
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
