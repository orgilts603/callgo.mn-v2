// Test-only helpers shared by the calls and live feature tests.
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { vi } from 'vitest'
import { useLive } from '@/lib/ws'
import type { Call, LiveEvent, TranscriptTurn } from '@/lib/types'

export function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } } })
}

export function wrapper(qc: QueryClient, initialEntries: string[] = ['/']) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={initialEntries}>{children}</MemoryRouter>
      </QueryClientProvider>
    )
  }
}

const iso = (offsetSec = 0) => new Date(Date.now() - offsetSec * 1000).toISOString()

export function makeCall(over: Partial<Call> = {}): Call {
  return {
    id: 'c1', orgId: 'o1', direction: 'inbound', status: 'active', fromNumber: '+97699112233', toNumber: '+97677001100',
    roomName: 'room-c1', startedAt: iso(65), answeredAt: iso(60), durationSec: 0, createdAt: iso(65), updatedAt: iso(60),
    ...over,
  }
}

export function makeTurn(over: Partial<TranscriptTurn> = {}): TranscriptTurn {
  return {
    id: 't1', callId: 'c1', seq: 1, speaker: 'customer', text: 'Сайн байна уу, би колгоу захиалмаар байна.', confidence: 0.92,
    startMs: 1200, endMs: 3400, isFinal: true, createdAt: iso(50), ...over,
  }
}

/**
 * Minimal WebSocket stand-in so tests can drive `useLive` exactly like the
 * server would: `connect()` then `emit(event)`.
 */
export class FakeSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: FakeSocket[] = []
  readyState = FakeSocket.CONNECTING
  url: string
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  constructor(url: string) { this.url = url; FakeSocket.instances.push(this) }
  send() {}
  close() { this.readyState = FakeSocket.CLOSED }
  open() { this.readyState = FakeSocket.OPEN; this.onopen?.() }
}

let seq = 0
/** Connect the live store to a FakeSocket; returns an `emit` helper. */
export function connectFakeLive() {
  localStorage.setItem('callgo.token', 'test-token')
  vi.stubGlobal('WebSocket', FakeSocket)
  useLive.getState().connect()
  const sock = FakeSocket.instances[FakeSocket.instances.length - 1]
  sock.open()
  return {
    socket: sock,
    emit(ev: Omit<LiveEvent, 'id' | 'orgId' | 'at'> & Partial<Pick<LiveEvent, 'id' | 'at'>>) {
      const full: LiveEvent = { id: `ev-${++seq}-${Math.random().toString(36).slice(2)}`, orgId: 'o1', at: new Date().toISOString(), ...ev }
      sock.onmessage?.({ data: JSON.stringify(full) })
    },
  }
}

export function resetLive() {
  useLive.getState().disconnect()
  useLive.setState({ activeCalls: {}, lastEvent: null, status: 'closed' })
  FakeSocket.instances = []
  localStorage.clear()
  vi.unstubAllGlobals()
}

/** vi.mock factory body for wavesurfer.js (jsdom has no canvas / media). */
export function createWaveSurferMock() {
  const handlers = new Map<string, (...a: unknown[]) => void>()
  const instance = {
    on: vi.fn((ev: string, fn: (...a: unknown[]) => void) => { handlers.set(ev, fn); return () => handlers.delete(ev) }),
    destroy: vi.fn(), setTime: vi.fn(), play: vi.fn(() => Promise.resolve()), playPause: vi.fn(() => Promise.resolve()),
    isPlaying: vi.fn(() => false), getDuration: vi.fn(() => 42), setPlaybackRate: vi.fn(),
    fire: (ev: string, ...args: unknown[]) => handlers.get(ev)?.(...args),
  }
  return { instance, create: vi.fn(() => instance) }
}
