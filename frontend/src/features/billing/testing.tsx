// Test helpers for billing (imported only by *.test.tsx).
import type { ReactNode } from 'react'
import { render } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { useLive } from '@/lib/ws'
import type { EventType, LiveEvent } from '@/lib/types'

export function makeClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } } })
}

function LocationProbe() {
  const loc = useLocation()
  return <div data-testid="location">{loc.pathname}{loc.search}</div>
}

export function renderWith(ui: ReactNode, { path = '/', client = makeClient() }: { path?: string; client?: QueryClient } = {}) {
  const utils = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes><Route path="*" element={<>{ui}<LocationProbe /></>} /></Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return { ...utils, client }
}

/** Replaces the live socket's onEvent with an in-memory emitter. */
export function fakeLive() {
  const listeners = new Set<(ev: LiveEvent) => void>()
  let seq = 0
  useLive.setState({
    onEvent: (fn) => { listeners.add(fn); return () => { listeners.delete(fn) } },
    connect: () => {}, disconnect: () => {},
  })
  return {
    emit(type: EventType, payload: unknown) {
      const ev: LiveEvent = { id: `ev-${++seq}`, type, orgId: 'o1', at: new Date().toISOString(), payload }
      listeners.forEach((fn) => fn(ev))
    },
    get size() { return listeners.size },
  }
}
