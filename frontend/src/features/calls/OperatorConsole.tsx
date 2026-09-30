import { useCallback, useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { toast } from 'sonner'
import { Bot, Headset, LogOut, Mic, MicOff, PhoneOff, User, Volume2 } from 'lucide-react'
import { Badge, Button, Card, type BadgeTone } from '@/components/ui'
import { useFocusTrap } from '@/components/ui/use-focus-trap'
import { api, HttpError } from '@/lib/api'
import { cn, fmtPhone } from '@/lib/utils'
import type { Call, HandoffToken, TranscriptPartialPayload, TranscriptTurn } from '@/lib/types'
import { isLiveStatus } from './api'
import { HangupDialog } from './CallActions'
import { Transcript } from './Transcript'
import { useLiveKitRoom, type ParticipantRole, type RoomPhase } from './useLiveKitRoom'

export interface OperatorConsoleProps {
  call: Call
  turns: TranscriptTurn[]
  partials?: TranscriptPartialPayload[]
  /** Called after the operator has left the room (or the console failed to start). */
  onClose: () => void
}

const PHASE: Record<RoomPhase, { label: string; tone: BadgeTone }> = {
  idle: { label: 'Бэлдэж байна', tone: 'neutral' },
  connecting: { label: 'Холбогдож байна…', tone: 'warning' },
  connected: { label: 'Холбогдсон', tone: 'success' },
  reconnecting: { label: 'Дахин холбогдож байна…', tone: 'warning' },
  disconnected: { label: 'Салсан', tone: 'neutral' },
  error: { label: 'Алдаа', tone: 'danger' },
}

const ROLE: Record<ParticipantRole, { label: string; icon: typeof User }> = {
  customer: { label: 'Харилцагч', icon: User },
  agent: { label: 'AI агент', icon: Bot },
  operator: { label: 'Оператор', icon: Headset },
}

/** Message for a failed `POST /calls/{id}/handoff`. */
export function handoffErrorMessage(err: unknown): string {
  if (err instanceof HttpError) {
    if (err.code === 'feature_unavailable' || err.status === 403) return 'Оператор дуудлагад орох боломж таны багцад ороогүй байна.'
    if (err.status === 404 || err.status === 409) return 'Дуудлага идэвхтэй биш байна.'
    if (err.status === 402) return 'Төлбөр төлөгдөөгүй тул үйлдэл хийх боломжгүй байна.'
  }
  return err instanceof Error && err.message ? err.message : 'Оператороор орж чадсангүй.'
}

/**
 * Full-screen operator take-over console. On mount it requests a LiveKit token
 * (`POST /calls/{id}/handoff`), joins the room with the microphone, and shows
 * participants next to the live transcript. Leaving (button, Esc or unmount)
 * disconnects and tells the backend (`POST /calls/{id}/handoff/end`) so the AI
 * agent resumes.
 */
export function OperatorConsole({ call, turns, partials, onClose }: OperatorConsoleProps) {
  const audioSink = useRef<HTMLDivElement>(null)
  const panel = useRef<HTMLDivElement>(null)
  const room = useLiveKitRoom(audioSink)
  const { connect, disconnect } = room
  const [attempt, setAttempt] = useState(0)
  const [startError, setStartError] = useState<string | null>(null)
  const [hangupOpen, setHangupOpen] = useState(false)
  const [leaving, setLeaving] = useState(false)

  const mounted = useRef(false)
  const handoffActive = useRef(false)
  const startedAttempt = useRef(-1)
  const callId = call.id
  const live = isLiveStatus(call.status)

  const endHandoff = useCallback(async () => {
    if (!handoffActive.current) return
    handoffActive.current = false
    try {
      await api.post<void>(`/calls/${encodeURIComponent(callId)}/handoff/end`)
    } catch (err) {
      toast.error(err instanceof Error && err.message ? err.message : 'Операторын холболтыг дуусгаж чадсангүй')
    }
  }, [callId])

  // Join: token → mic → room. Guarded per attempt so StrictMode's double effect posts once.
  useEffect(() => {
    mounted.current = true
    if (startedAttempt.current === attempt) return
    startedAttempt.current = attempt
    setStartError(null)
    void (async () => {
      let token: HandoffToken
      try {
        token = await api.post<HandoffToken>(`/calls/${encodeURIComponent(callId)}/handoff`)
      } catch (err) {
        setStartError(handoffErrorMessage(err))
        return
      }
      handoffActive.current = true
      if (!mounted.current) { await endHandoff(); return }
      const ok = await connect({ url: token.url, token: token.token })
      if (!ok) await endHandoff() // connection error is shown from `room.error`
    })()
  }, [attempt, callId, connect, endHandoff])

  // Unmount (e.g. the drawer was closed): release the room and hand the call back to the agent.
  useEffect(() => () => {
    mounted.current = false
    if (handoffActive.current) {
      handoffActive.current = false
      void api.post<void>(`/calls/${encodeURIComponent(callId)}/handoff/end`).catch(() => undefined)
    }
  }, [callId])

  const leave = useCallback(async () => {
    setLeaving(true)
    await disconnect()
    await endHandoff()
    onClose()
  }, [disconnect, endHandoff, onClose])

  // The call ended while we were connected: drop the room, keep the transcript visible.
  const ended = !live
  useEffect(() => {
    if (ended) { void disconnect().then(endHandoff) }
  }, [ended, disconnect, endHandoff])

  useEffect(() => {
    // Capture phase + stopPropagation: Esc leaves the room without also closing the drawer underneath.
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || hangupOpen) return
      e.stopPropagation()
      void leave()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [leave, hangupOpen])
  useFocusTrap(true, panel)

  const retry = () => { setStartError(null); setAttempt((a) => a + 1) }
  const errorText = startError ?? room.error?.message ?? null
  const phase = startError ? 'error' : room.phase
  const p = PHASE[phase]
  const joined = room.phase === 'connected' || room.phase === 'reconnecting'

  // Portalled: the drawer's transform would otherwise become the containing block of `fixed`.
  return createPortal(
    <div className="fixed inset-0 z-[45] flex flex-col bg-[var(--surface-0)]" role="dialog" aria-modal="true" aria-label="Оператор дуудлагад орсон" ref={panel} tabIndex={-1}>
      <header className="flex shrink-0 flex-wrap items-center gap-3 border-b border-[var(--border)] bg-[var(--surface-1)] px-4 py-3">
        <Headset className="h-4 w-4 text-[var(--fg-muted)]" aria-hidden />
        <h2 className="text-sm font-semibold text-[var(--fg)]">Дуудлагад орсон</h2>
        <span className="text-xs text-[var(--fg-muted)]">{fmtPhone(call.direction === 'inbound' ? call.fromNumber : call.toNumber)}</span>
        <Badge tone={p.tone} dot pulse={phase === 'connecting' || phase === 'reconnecting'} data-testid="room-phase">{p.label}</Badge>
        {ended && <Badge tone="neutral">Дуудлага дууссан</Badge>}
      </header>

      <div className="grid min-h-0 flex-1 gap-4 overflow-y-auto p-4 lg:grid-cols-[minmax(280px,360px)_1fr] lg:overflow-hidden">
        <div className="space-y-4 lg:overflow-y-auto">
          {errorText && (
            <div role="alert" className="rounded-[var(--radius)] border border-[var(--danger-border)] bg-[var(--danger-soft)] px-3 py-2.5 text-xs text-[var(--danger-fg)]">
              <p>{errorText}</p>
              {!leaving && <Button className="mt-2" size="sm" variant="outline" onClick={retry}>Дахин оролдох</Button>}
            </div>
          )}

          <Card className="space-y-3 px-4 py-3.5">
            <div className="flex flex-wrap gap-2">
              <Button variant={room.muted ? 'danger' : 'secondary'} size="sm" disabled={!joined} onClick={room.toggleMute}
                aria-pressed={room.muted} aria-label={room.muted ? 'Микрофон нээх' : 'Микрофон хаах'}>
                {room.muted ? <MicOff /> : <Mic />} {room.muted ? 'Дуугүй' : 'Микрофон'}
              </Button>
              {live && (
                <Button variant="danger" size="sm" onClick={() => setHangupOpen(true)}>
                  <PhoneOff /> Дуудлага таслах
                </Button>
              )}
              <Button variant="outline" size="sm" onClick={() => void leave()} loading={leaving}>
                <LogOut /> Гарах
              </Button>
            </div>
            <label className="flex items-center gap-2 text-xs text-[var(--fg-muted)]">
              <Volume2 className="h-3.5 w-3.5 shrink-0" aria-hidden />
              <input type="range" min={0} max={100} step={1} value={Math.round(room.volume * 100)} aria-label="Дууны түвшин"
                onChange={(e) => room.setVolume(Number(e.target.value) / 100)} className="h-1.5 w-full cursor-pointer accent-[var(--accent)]" />
              <span className="tabular w-9 text-right">{Math.round(room.volume * 100)}%</span>
            </label>
          </Card>

          <Card className="overflow-hidden">
            <div className="border-b border-[var(--border-subtle)] px-4 py-2.5 text-xs font-semibold text-[var(--fg)]">Оролцогчид</div>
            {room.participants.length === 0 ? (
              <p className="px-4 py-4 text-xs text-[var(--fg-subtle)]">{joined || room.phase === 'connecting' ? 'Оролцогч алга' : 'Холбогдоогүй байна'}</p>
            ) : (
              <ul className="divide-y divide-[var(--border-subtle)]" aria-label="Оролцогчид">
                {room.participants.map((pt) => {
                  const r = ROLE[pt.role]
                  const Icon = r.icon
                  return (
                    <li key={pt.identity} className="flex items-center gap-2.5 px-4 py-2.5 text-xs" data-testid="participant" data-role={pt.role} data-speaking={pt.speaking}>
                      <span className={cn('flex h-6 w-6 items-center justify-center rounded-full border', pt.speaking ? 'border-[var(--success-border)] bg-[var(--success-soft)] text-[var(--success-fg)]' : 'border-[var(--border)] bg-[var(--surface-2)] text-[var(--fg-muted)]')}>
                        <Icon className="h-3.5 w-3.5" aria-hidden />
                      </span>
                      <span className="min-w-0 flex-1 truncate text-[var(--fg)]">{r.label}{pt.isLocal ? ' (та)' : ''}<span className="ml-1.5 text-[var(--fg-subtle)]">{pt.name}</span></span>
                      {pt.speaking && (
                        <span className="flex items-center gap-1.5 text-[var(--success-fg)]" role="status">
                          <span className="relative flex h-1.5 w-1.5" aria-hidden>
                            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-[var(--success)] opacity-75" />
                            <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-[var(--success)]" />
                          </span>
                          ярьж байна
                        </span>
                      )}
                    </li>
                  )
                })}
              </ul>
            )}
          </Card>
        </div>

        <Card className="flex min-h-[320px] min-w-0 flex-col overflow-hidden lg:min-h-0">
          <div className="border-b border-[var(--border-subtle)] px-4 py-2.5 text-xs font-semibold text-[var(--fg)]">Яриа</div>
          <Transcript className="min-h-0 flex-1" callId={callId} turns={turns} partials={partials} live={live} />
        </Card>
      </div>

      {/* Remote audio is attached here by useLiveKitRoom. */}
      <div ref={audioSink} className="hidden" aria-hidden data-testid="audio-sink" />
      {hangupOpen && <HangupDialog call={call} onClose={() => setHangupOpen(false)} onDone={() => setHangupOpen(false)} />}
    </div>,
    document.body,
  )
}
