import type { RouteObject } from 'react-router-dom'
import { CallbacksPage } from './CallbacksPage'

export const routes: RouteObject[] = [{ path: '/callbacks', element: <CallbacksPage /> }]
