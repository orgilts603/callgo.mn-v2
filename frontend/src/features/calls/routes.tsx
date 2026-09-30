import type { RouteObject } from 'react-router-dom'
import { CallPage } from './CallPage'

export const routes: RouteObject[] = [{ path: '/calls/:id', element: <CallPage /> }]
