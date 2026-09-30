import type { RouteObject } from 'react-router-dom'
import AnalyticsPage from './AnalyticsPage'

export const routes: RouteObject[] = [{ path: '/analytics', element: <AnalyticsPage />, handle: { crumb: 'Аналитик' } }]
