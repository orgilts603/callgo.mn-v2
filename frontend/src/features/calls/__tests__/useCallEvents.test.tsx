import { act, renderHook } from '@testing-library/react'
import { useCallEvents } from '../useCallEvents'
import { connectFakeLive, makeTurn, resetLive } from '../testUtils'

describe('useCallEvents', () => {
  afterEach(resetLive)

  it('tracks partials, agent state and final turns for one call', () => {
    const live = connectFakeLive()
    const { result, rerender } = renderHook(({ id }) => useCallEvents(id), { initialProps: { id: 'c1' as string | null } })

    act(() => {
      live.emit({ type: 'transcript.partial', callId: 'c1', payload: { speaker: 'customer', text: 'сайн', startMs: 0 } })
      live.emit({ type: 'transcript.partial', callId: 'c2', payload: { speaker: 'customer', text: 'other call', startMs: 0 } })
      live.emit({ type: 'agent.state', callId: 'c1', payload: { state: 'listening', llmModel: 'google/gemini-2.5-flash' } })
    })
    expect(result.current.partials).toEqual([{ speaker: 'customer', text: 'сайн', startMs: 0 }])
    expect(result.current.agentState).toBe('listening')

    const turn = makeTurn({ id: 't9', text: 'сайн байна уу' })
    act(() => { live.emit({ type: 'transcript.final', callId: 'c1', payload: { turn } }) })
    expect(result.current.lastTurn).toEqual(turn)
    expect(result.current.partials).toEqual([])

    rerender({ id: 'c2' })
    expect(result.current.agentState).toBeNull()
    expect(result.current.lastTurn).toBeNull()
  })
})
