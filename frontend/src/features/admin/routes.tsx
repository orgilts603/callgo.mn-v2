import type { RouteObject } from 'react-router-dom'
import AdminPage from './AdminPage'

// AdminPage redirects to "/" itself unless user.isPlatformAdmin.
export const routes: RouteObject[] = [{ path: '/admin', element: <AdminPage />, handle: { crumb: 'Платформ админ' } }]
