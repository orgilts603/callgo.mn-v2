// Route registry. Owned by the integrator; features export `routes` from
// src/features/<name>/routes.tsx and are appended here at integration time.
import type { RouteObject } from 'react-router-dom'

export const featureRoutes: RouteObject[] = []
