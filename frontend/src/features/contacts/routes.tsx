import type { RouteObject } from 'react-router-dom'
import { ContactsPage } from './ContactsPage'
import { ContactDetailPage } from './ContactDetailPage'

export const routes: RouteObject[] = [
  { path: '/contacts', element: <ContactsPage /> },
  { path: '/contacts/:id', element: <ContactDetailPage /> },
]
