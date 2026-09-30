import type { UIMatch } from 'react-router-dom'

/**
 * Optional `handle` a feature route may set in its RouteObject:
 *   { path: 'calls/:id', element: <CallDetail/>, handle: { crumb: 'Дэлгэрэнгүй' } }
 * - crumb: breadcrumb label (string, or a function of the match, e.g. to show an id)
 * - fullWidth: render the page edge-to-edge (no max-width / padding container)
 */
export interface RouteHandle {
  crumb?: string | ((match: UIMatch) => string)
  fullWidth?: boolean
}

export function getHandle(match: UIMatch): RouteHandle | undefined {
  const h = match.handle
  return h && typeof h === 'object' ? (h as RouteHandle) : undefined
}
