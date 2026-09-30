// Route registry: every feature exports `routes: RouteObject[]` from its
// routes.tsx and is aggregated here. The app shell spreads `featureRoutes`
// under the protected layout (see src/app/router.tsx).
import type { RouteObject } from 'react-router-dom'
import { routes as liveRoutes } from './live/routes'
import { routes as callsRoutes } from './calls/routes'
import { routes as historyRoutes } from './history/routes'
import { routes as contactsRoutes } from './contacts/routes'
import { routes as campaignsRoutes } from './campaigns/routes'
import { routes as lexiconRoutes } from './lexicon/routes'
import { routes as settingsRoutes } from './settings/routes'

export const featureRoutes: RouteObject[] = [
  ...liveRoutes,
  ...historyRoutes,
  ...callsRoutes,
  ...contactsRoutes,
  ...campaignsRoutes,
  ...lexiconRoutes,
  ...settingsRoutes,
]
