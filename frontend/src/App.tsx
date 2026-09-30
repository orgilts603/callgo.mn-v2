import { useState } from 'react'
import { RouterProvider } from 'react-router-dom'
import { Providers } from '@/app/providers'
import { createAppRouter } from '@/app/router'
import { ErrorBoundary } from '@/components/layout/ErrorBoundary'

export default function App() {
  const [router] = useState(() => createAppRouter())
  return (
    <ErrorBoundary>
      <Providers>
        <RouterProvider router={router} />
      </Providers>
    </ErrorBoundary>
  )
}
