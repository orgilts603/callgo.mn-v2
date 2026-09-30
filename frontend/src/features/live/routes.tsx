import type { RouteObject } from 'react-router-dom'
import { LiveDeskPage } from './LiveDeskPage'

export const routes: RouteObject[] = [{ path: '/live', element: <LiveDeskPage /> }]
