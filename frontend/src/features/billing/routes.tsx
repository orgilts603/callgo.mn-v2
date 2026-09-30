import type { RouteObject } from 'react-router-dom'
import { Navigate, useLocation } from 'react-router-dom'
import { BILLING_PATH } from './quota'

/**
 * `/settings/billing` itself is rendered by SettingsPage (tab "Төлбөр" →
 * BillingPage) through the existing `/settings/:tab` route, so the settings
 * chrome stays. A static `/settings/billing` route here would outrank
 * `/settings/:tab` and drop the tab bar, so this module only adds the short
 * alias `/billing` (keeps `?view=`).
 */
function BillingRedirect() {
  const { search } = useLocation()
  return <Navigate to={{ pathname: BILLING_PATH, search }} replace />
}

export const routes: RouteObject[] = [
  { path: '/billing', element: <BillingRedirect />, handle: { crumb: 'Төлбөр' } },
]
