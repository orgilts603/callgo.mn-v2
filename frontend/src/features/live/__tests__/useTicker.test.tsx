import { act, render, screen } from '@testing-library/react'
import { useTicker } from '../useTicker'

function Clock({ label }: { label: string }) {
  const now = useTicker(1000)
  return <span data-testid={label}>{now}</span>
}

describe('useTicker', () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-09-30T10:00:00Z')) })
  afterEach(() => { vi.useRealTimers() })

  it('shares one interval between subscribers and ticks every second', () => {
    const spy = vi.spyOn(globalThis, 'setInterval')
    const { unmount } = render(<><Clock label="a" /><Clock label="b" /><Clock label="c" /></>)
    expect(spy).toHaveBeenCalledTimes(1)
    const start = Number(screen.getByTestId('a').textContent)
    act(() => { vi.advanceTimersByTime(3000) })
    expect(Number(screen.getByTestId('a').textContent)).toBe(start + 3000)
    expect(screen.getByTestId('b').textContent).toBe(screen.getByTestId('a').textContent)
    const clear = vi.spyOn(globalThis, 'clearInterval')
    unmount()
    expect(clear).toHaveBeenCalledTimes(1)
    spy.mockRestore(); clear.mockRestore()
  })
})
