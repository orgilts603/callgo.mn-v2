// Live event socket (zustand). FROZEN CONTRACT — features subscribe via useLive / onEvent.
import { create } from 'zustand'
import { getToken } from './api'
import type { Call, LiveEvent, SystemPayload } from './types'

type Listener = (ev: LiveEvent) => void
type Status = 'connecting' | 'open' | 'closed'

interface LiveState {
  status: Status
  activeCalls: Record<string, Call>
  lastEvent: LiveEvent | null
  connect: () => void
  disconnect: () => void
  onEvent: (fn: Listener) => () => void
}

let socket: WebSocket | null = null
let retry = 0
let timer: ReturnType<typeof setTimeout> | null = null
const listeners = new Set<Listener>()
const seen = new Set<string>()

export const useLive = create<LiveState>((set, get) => ({
  status: 'closed',
  activeCalls: {},
  lastEvent: null,
  connect: () => {
    if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) return
    const token = getToken()
    if (!token) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    socket = new WebSocket(`${proto}://${location.host}/api/ws?token=${encodeURIComponent(token)}`)
    set({ status: 'connecting' })
    socket.onopen = () => { retry = 0; set({ status: 'open' }) }
    socket.onclose = () => {
      set({ status: 'closed' })
      socket = null
      if (!getToken()) return
      const delay = Math.min(30000, 1000 * 2 ** retry++)
      timer = setTimeout(() => get().connect(), delay)
    }
    socket.onmessage = (m) => {
      let ev: LiveEvent
      try { ev = JSON.parse(m.data) } catch { return }
      if (ev.type === 'pong' as string) return
      if (ev.id) { if (seen.has(ev.id)) return; seen.add(ev.id); if (seen.size > 5000) seen.clear() }
      const calls = { ...get().activeCalls }
      if (ev.type === 'system') {
        const p = ev.payload as SystemPayload
        if (p.hello) { for (const k of Object.keys(calls)) delete calls[k]; for (const c of p.activeCalls ?? []) calls[c.id] = c }
      } else if (ev.type.startsWith('call.')) {
        const call = (ev.payload as { call?: Call }).call
        if (call) {
          if (['completed', 'failed', 'no_answer', 'busy', 'voicemail'].includes(call.status)) delete calls[call.id]
          else calls[call.id] = call
        }
      }
      set({ activeCalls: calls, lastEvent: ev })
      listeners.forEach((fn) => fn(ev))
    }
  },
  disconnect: () => { if (timer) clearTimeout(timer); socket?.close(); socket = null; set({ status: 'closed' }) },
  onEvent: (fn) => { listeners.add(fn); return () => listeners.delete(fn) },
}))
