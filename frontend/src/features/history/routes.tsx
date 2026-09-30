import type { RouteObject } from 'react-router-dom'
import { CallHistoryPage } from './CallHistoryPage'

export const routes: RouteObject[] = [{ path: '/calls', element: <CallHistoryPage /> }]
