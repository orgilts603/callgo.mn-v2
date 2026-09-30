import type { RouteObject } from 'react-router-dom'
import { CampaignsPage } from './CampaignsPage'
import { CampaignDetailPage } from './CampaignDetailPage'

export const routes: RouteObject[] = [
  { path: '/campaigns', element: <CampaignsPage /> },
  { path: '/campaigns/:id', element: <CampaignDetailPage /> },
]
