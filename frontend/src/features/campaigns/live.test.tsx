import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { Campaign, LiveEvent } from '@/lib/types'

const listeners = new Set<(e: LiveEvent) => void>()
const onEvent = (fn: (e: LiveEvent) => void) => { listeners.add(fn); return () => { listeners.delete(fn) } }
vi.mock('@/lib/ws', () => ({
  useLive: (sel: (s: { onEvent: typeof onEvent }) => unknown) => sel({ onEvent }),
}))

import { CampaignsPage } from './CampaignsPage'

const now = new Date().toISOString()
const c1 = {
  id: 'c1', orgId: 'o', name: 'Хураамж', script: '', status: 'running', sipNumberId: null, agentProfileId: null,
  concurrency: 5, maxAttempts: 2, total: 20, completed: 8, failed: 2, createdAt: now, updatedAt: now,
} as unknown as Campaign

describe('campaign.progress live updates', () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks(); listeners.clear() })

  it('patches the campaigns cache from the event payload', async () => {
    vi.spyOn(api, 'get').mockImplementation(async (path: string) => ({ items: path === '/campaigns' ? [c1] : [] }) as never)
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(<QueryClientProvider client={qc}><MemoryRouter><CampaignsPage /></MemoryRouter></QueryClientProvider>)
    const row = (await screen.findByText('Хураамж')).closest('tr')!
    expect(within(row).getByText('10/20')).toBeInTheDocument()

    const ev: LiveEvent = { id: 'e1', type: 'campaign.progress', orgId: 'o', at: now, payload: { campaign: { ...c1, completed: 15, failed: 5 }, target: null } }
    act(() => { listeners.forEach((fn) => fn(ev)) })
    expect(await within(row).findByText('20/20')).toBeInTheDocument()
    expect(within(row).getByText('100%')).toBeInTheDocument()
  })
})
