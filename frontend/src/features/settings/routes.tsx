import type { RouteObject } from 'react-router-dom'
import SettingsPage from './SettingsPage'

export const routes: RouteObject[] = [
  { path: '/settings', element: <SettingsPage /> },
  { path: '/settings/:tab', element: <SettingsPage /> },
]
