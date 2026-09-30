import { createBrowserRouter, type RouteObject } from 'react-router-dom'
import { featureRoutes } from '@/features/routes'
import { AppShell } from '@/components/layout/AppShell'
import { RouteErrorBoundary } from '@/components/layout/ErrorBoundary'
import LoginPage from '@/features/auth/LoginPage'
import SignupPage from '@/features/auth/SignupPage'
import VerifyEmailPage from '@/features/auth/VerifyEmailPage'
import ForgotPasswordPage from '@/features/auth/ForgotPasswordPage'
import ResetPasswordPage from '@/features/auth/ResetPasswordPage'
import AcceptInvitationPage from '@/features/auth/AcceptInvitationPage'
import DashboardPage from '@/features/dashboard/DashboardPage'
import NotFoundPage from './NotFoundPage'
import { RequireAuth } from './RequireAuth'

/**
 * Route tree:
 *   /login                         LoginPage (public)
 *   /signup, /verify-email, /forgot-password, /reset-password, /accept-invitation
 *                                  public auth pages (token pages read `?token=`)
 *   <RequireAuth><AppShell/>       protected layout (sidebar + topbar)
 *     └─ (pathless, errorElement)  page-level error boundary, keeps the shell
 *          ├─ index               DashboardPage
 *          ├─ ...featureRoutes    appended by the integrator in features/routes.ts
 *          └─ *                   NotFoundPage
 * Feature routes may use relative ("calls/:id") or absolute ("/calls/:id") paths
 * and an optional `handle: RouteHandle` ({ crumb, fullWidth }).
 */
/** Public (no auth) pages besides /login. */
export const publicRoutes: RouteObject[] = [
  { path: '/signup', element: <SignupPage />, errorElement: <RouteErrorBoundary /> },
  { path: '/verify-email', element: <VerifyEmailPage />, errorElement: <RouteErrorBoundary /> },
  { path: '/forgot-password', element: <ForgotPasswordPage />, errorElement: <RouteErrorBoundary /> },
  { path: '/reset-password', element: <ResetPasswordPage />, errorElement: <RouteErrorBoundary /> },
  { path: '/accept-invitation', element: <AcceptInvitationPage />, errorElement: <RouteErrorBoundary /> },
]

export function buildRoutes(extra: RouteObject[] = featureRoutes): RouteObject[] {
  return [
    { path: '/login', element: <LoginPage />, errorElement: <RouteErrorBoundary /> },
    ...publicRoutes,
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
