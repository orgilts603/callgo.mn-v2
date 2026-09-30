import { useSyncExternalStore } from 'react'

/**
 * Shared clocks: every component that calls `useTicker(1000)` subscribes to the
 * same single `setInterval`, so a table with 50 ticking cells still runs one
 * timer. The interval is cleared when the last subscriber unmounts.
 */
interface Clock {
  now: number
  listeners: Set<() => void>
  timer: ReturnType<typeof setInterval> | null
  subscribe: (fn: () => void) => () => void
  getSnapshot: () => number
}

const clocks = new Map<number, Clock>()

function getClock(intervalMs: number): Clock {
  let clock = clocks.get(intervalMs)
  if (clock) return clock
  const c: Clock = {
    now: Date.now(),
    listeners: new Set(),
    timer: null,
    subscribe: (fn) => {
      c.listeners.add(fn)
      if (!c.timer) {
        c.now = Date.now()
        c.timer = setInterval(() => {
          c.now = Date.now()
          c.listeners.forEach((l) => l())
        }, intervalMs)
      }
      return () => {
        c.listeners.delete(fn)
        if (c.listeners.size === 0 && c.timer) {
          clearInterval(c.timer)
          c.timer = null
        }
      }
    },
    // While no timer runs the value may be stale; refresh it at most once per period
    // (stable between consecutive reads, as useSyncExternalStore requires).
    getSnapshot: () => {
      if (!c.timer) {
        const t = Date.now()
        if (t - c.now >= intervalMs) c.now = t
      }
      return c.now
    },
  }
  clock = c
  clocks.set(intervalMs, clock)
  return clock
}

/** Returns `Date.now()` refreshed every `intervalMs` (shared interval per period). */
export function useTicker(intervalMs = 1000): number {
  const clock = getClock(Math.max(16, Math.floor(intervalMs)))
  return useSyncExternalStore(clock.subscribe, clock.getSnapshot, clock.getSnapshot)
}
