import { createBrowserRouter, type RouteObject } from 'react-router-dom'
import { featureRoutes } from '@/features/routes'
import { AppShell } from '@/components/layout/AppShell'
import { RouteErrorBoundary } from '@/components/layout/ErrorBoundary'
import LoginPage from '@/features/auth/LoginPage'
import DashboardPage from '@/features/dashboard/DashboardPage'
import NotFoundPage from './NotFoundPage'
import { RequireAuth } from './RequireAuth'

/**
 * Route tree:
 *   /login                         LoginPage (public)
 *   <RequireAuth><AppShell/>       protected layout (sidebar + topbar)
 *     └─ (pathless, errorElement)  page-level error boundary, keeps the shell
 *          ├─ index               DashboardPage
 *          ├─ ...featureRoutes    appended by the integrator in features/routes.ts
 *          └─ *                   NotFoundPage
 * Feature routes may use relative ("calls/:id") or absolute ("/calls/:id") paths
 * and an optional `handle: RouteHandle` ({ crumb, fullWidth }).
 */
export function buildRoutes(extra: RouteObject[] = featureRoutes): RouteObject[] {
  return [
    { path: '/login', element: <LoginPage />, errorElement: <RouteErrorBoundary /> },
    {
      element: <RequireAuth><AppShell /></RequireAuth>,
      errorElement: <RouteErrorBoundary />,
      children: [
        {
          errorElement: <RouteErrorBoundary />,
          children: [
            { index: true, element: <DashboardPage />, handle: { crumb: 'Хяналтын самбар' } },
            ...extra,
            { path: '*', element: <NotFoundPage />, handle: { crumb: 'Хуудас олдсонгүй' } },
          ],
        },
      ],
    },
  ]
}

export function createAppRouter(routes: RouteObject[] = buildRoutes()) {
  return createBrowserRouter(routes)
}
