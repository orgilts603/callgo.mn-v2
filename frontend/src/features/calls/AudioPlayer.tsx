import { useEffect, useImperativeHandle, useRef, useState, type Ref } from 'react'
import { useQuery } from '@tanstack/react-query'
import WaveSurfer from 'wavesurfer.js'
import { AlertCircle, Lock, Mic, Pause, Play, VolumeX } from 'lucide-react'
import { Button, Skeleton } from '@/components/ui'
import { api, HttpError } from '@/lib/api'
import { cn, fmtDuration } from '@/lib/utils'
import type { RecordingInfo } from '@/lib/types'

/** Response of `GET /api/calls/{id}/recording/url`. */
export interface RecordingUrl { url: string; expiresAt: string }

export const recordingUrlKey = (callId: string) => ['call', callId, 'recording-url'] as const

/** Whether a recording can be played back: finished, or legacy calls that only carry `recordingUrl`. */
export function isRecordingReady(recording: RecordingInfo | null | undefined, recordingUrl?: string | null): boolean {
  return recording ? recording.status === 'ready' : !!recordingUrl
}

/**
 * Signed playback URL for a call's recording (TTL ~10 min). It is fetched each
 * time the player opens (no cache between mounts, so a stale link is never
 * reused); `refetch()` asks for a fresh one when playback hits an expired link.
 */
export function useRecordingUrl(callId: string | null | undefined, enabled = true) {
  return useQuery({
    queryKey: recordingUrlKey(callId ?? ''),
    queryFn: () => api.get<RecordingUrl>(`/calls/${encodeURIComponent(callId as string)}/recording/url`),
    enabled: !!callId && enabled,
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
}

export interface AudioPlayerHandle {
  /** Seek to a position in milliseconds and start playback. */
  seekMs: (ms: number) => void
}

export interface AudioPlayerProps {
  callId: string
  /** `call.recording`: drives the recording / ready / failed states. */
  recording?: RecordingInfo | null
  /** Legacy `call.recordingUrl` (present without `recording` on older calls). */
  recordingUrl?: string | null
  /** Shown in the empty state when there is no recording yet. */
  live?: boolean
  /** Playback position callback (throttled to ~4 updates per second). */
  onTime?: (ms: number) => void
  ref?: Ref<AudioPlayerHandle>
  className?: string
}

const PLAYBACK_RATES = [1, 1.5, 2] as const

function cssVar(name: string, fallback: string): string {
  if (typeof window === 'undefined') return fallback
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

interface WavePlayerProps { url: string; onTime?: (ms: number) => void; onLoadError: () => void; ref?: Ref<AudioPlayerHandle>; className?: string }

/** Waveform player for one signed URL (wavesurfer.js). A new `url` reloads and resumes at the same position. */
function WavePlayer({ url, onTime, onLoadError, ref, className }: WavePlayerProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const wsRef = useRef<WaveSurfer | null>(null)
  const pendingSeek = useRef<number | null>(null)
  const onTimeRef = useRef(onTime)
  const onLoadErrorRef = useRef(onLoadError)
  const lastTime = useRef(0)
  const wasPlaying = useRef(false)
  const [ready, setReady] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [playing, setPlaying] = useState(false)
  const [duration, setDuration] = useState(0)
  const [current, setCurrent] = useState(0)
  const [rate, setRate] = useState<number>(1)

  useEffect(() => { onTimeRef.current = onTime }, [onTime])
  useEffect(() => { onLoadErrorRef.current = onLoadError }, [onLoadError])

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    const resumeAt = lastTime.current
    const resumePlaying = wasPlaying.current
    setReady(false); setError(null); setPlaying(false); setDuration(0)
    const ws = WaveSurfer.create({
      container,
      url,
      height: 56,
      waveColor: cssVar('--surface-3', '#202a55'),
      progressColor: cssVar('--accent', '#6366f1'),
      cursorColor: cssVar('--accent-fg', '#c7d2fe'),
      cursorWidth: 2,
      barWidth: 2,
      barGap: 1,
      barRadius: 2,
      dragToSeek: true,
      normalize: true,
    })
    wsRef.current = ws
    let lastQuarter = -1
    const emitTime = (t: number) => {
      const q = Math.floor(t * 4)
      if (q === lastQuarter) return
      lastQuarter = q
      lastTime.current = t
      setCurrent(t)
      onTimeRef.current?.(Math.round(t * 1000))
    }
    const subs = [
      ws.on('ready', (d) => {
        setDuration(d); setReady(true)
        if (pendingSeek.current != null) { ws.setTime(pendingSeek.current / 1000); pendingSeek.current = null; void ws.play() }
        else if (resumeAt > 0) { ws.setTime(resumeAt); if (resumePlaying) void ws.play() }
      }),
      ws.on('timeupdate', emitTime),
      ws.on('play', () => { wasPlaying.current = true; setPlaying(true) }),
      ws.on('pause', () => { wasPlaying.current = false; setPlaying(false) }),
      ws.on('finish', () => { wasPlaying.current = false; setPlaying(false) }),
      // An expired / rejected signed URL: ask the parent for a fresh one (it reloads us with the new url).
      ws.on('error', (e) => { setError(e?.message || 'Бичлэг ачаалж чадсангүй'); onLoadErrorRef.current() }),
    ]
    return () => {
      subs.forEach((un) => un())
      ws.destroy()
      wsRef.current = null
    }
  }, [url])

  useImperativeHandle(ref, () => ({
    seekMs: (ms: number) => {
      const ws = wsRef.current
      if (!ws) return
      if (!ws.getDuration()) { pendingSeek.current = ms; return }
      ws.setTime(ms / 1000)
      if (!ws.isPlaying()) void ws.play()
    },
  }), [])

  const changeRate = (r: number) => {
    setRate(r)
    wsRef.current?.setPlaybackRate(r, true)
  }

  return (
    <div className={cn('rounded-lg border border-[var(--border)] bg-[var(--surface-inset)] p-3', className)}>
      <div className="flex items-center gap-3">
        <Button size="icon" variant="primary" className="h-9 w-9 shrink-0 rounded-full" disabled={!ready}
          onClick={() => void wsRef.current?.playPause()} aria-label={playing ? 'Түр зогсоох' : 'Тоглуулах'}>
          {playing ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4 translate-x-px" />}
        </Button>
        <div className="relative min-w-0 flex-1">
          <div ref={containerRef} className={cn('w-full', !ready && !error && 'opacity-0')} data-testid="waveform" />
          {!ready && !error && <Skeleton className="absolute inset-0 h-14" />}
          {error && (
            <div className="absolute inset-0 flex items-center gap-2 text-xs text-[var(--danger-fg)]">
              <AlertCircle className="h-4 w-4" />{error}
            </div>
          )}
        </div>
      </div>
      <div className="mt-2 flex items-center justify-between gap-3 text-[11px] text-[var(--fg-muted)]">
        <span className="tabular-nums">{fmtDuration(current)} / {fmtDuration(duration)}</span>
        <div className="flex items-center gap-1" role="group" aria-label="Тоглуулах хурд">
          {PLAYBACK_RATES.map((r) => (
            <button key={r} type="button" onClick={() => changeRate(r)} aria-pressed={rate === r}
              className={cn('rounded px-1.5 py-0.5 tabular-nums transition-colors',
                rate === r ? 'bg-[var(--accent-soft)] text-[var(--accent-fg)]' : 'hover:bg-[var(--surface-2)] hover:text-[var(--fg)]')}>
              {r}x
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

const boxClass = 'flex items-center gap-3 rounded-lg border border-dashed border-[var(--border)] px-4 py-3 text-xs text-[var(--fg-muted)]'

/**
 * Call recording player. Fetches a signed URL from `/calls/{id}/recording/url`
 * and renders one of: nothing recorded, recording in progress (pulse), ready
 * (waveform), failed. The URL is refreshed once if playback fails to load
 * (expired link / 403).
 */
export function AudioPlayer({ callId, recording, recordingUrl, live, onTime, ref, className }: AudioPlayerProps) {
  const status = recording?.status
  const ready = isRecordingReady(recording, recordingUrl)
  const signed = useRecordingUrl(callId, ready)
  const retried = useRef<string | null>(null)
  const refetch = signed.refetch

  const onLoadError = () => {
    // One forced refresh per URL; otherwise let the waveform show its error.
    const cur = signed.data?.url ?? null
    if (retried.current === cur) return
    retried.current = cur
    void refetch()
  }

  if (status === 'recording') {
    return (
      <div className={cn(boxClass, 'border-solid', className)} data-testid="audio-recording" role="status">
        <span className="relative flex h-2.5 w-2.5" aria-hidden>
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-[var(--danger)] opacity-75" />
          <span className="relative inline-flex h-2.5 w-2.5 rounded-full bg-[var(--danger)]" />
        </span>
        <Mic className="h-4 w-4 text-[var(--fg-subtle)]" aria-hidden />
        Бичлэг хийгдэж байна…
      </div>
    )
  }
  if (status === 'failed') {
    return (
      <div className={cn(boxClass, 'border-[var(--danger-border)] text-[var(--danger-fg)]', className)} data-testid="audio-failed" role="alert">
        <AlertCircle className="h-4 w-4" aria-hidden /> Бичлэг хийж чадсангүй.
      </div>
    )
  }
  if (!ready) {
    const text = status === 'deleted'
      ? 'Бичлэгийг хадгалалтын хугацааны дагуу устгасан.'
      : live ? 'Дуудлага дууссаны дараа бичлэг гарна.' : 'Энэ дуудлагад бичлэг байхгүй.'
    return (
      <div className={cn(boxClass, className)} data-testid="audio-empty">
        <VolumeX className="h-4 w-4 text-[var(--fg-subtle)]" aria-hidden />{text}
      </div>
    )
  }
  if (signed.isError) {
    const err = signed.error
    const notFound = err instanceof HttpError && err.status === 404
    const unavailable = err instanceof HttpError && err.code === 'feature_unavailable'
    return (
      <div className={cn(boxClass, !notFound && 'border-[var(--danger-border)]', className)} data-testid={notFound ? 'audio-empty' : 'audio-error'} role={notFound ? undefined : 'alert'}>
        {unavailable ? <Lock className="h-4 w-4" aria-hidden /> : <AlertCircle className="h-4 w-4 text-[var(--danger-fg)]" aria-hidden />}
        <span className="flex-1">
          {notFound ? 'Энэ дуудлагад бичлэг байхгүй.' : unavailable ? 'Бичлэг сонсох боломж таны багцад ороогүй байна.' : 'Бичлэгийн холбоос авч чадсангүй.'}
        </span>
        {!notFound && !unavailable && <Button size="sm" variant="outline" onClick={() => void refetch()}>Дахин оролдох</Button>}
      </div>
    )
  }
  if (!signed.data?.url) {
    return <div className={cn('rounded-lg border border-[var(--border)] bg-[var(--surface-inset)] p-3', className)} data-testid="audio-loading"><Skeleton className="h-14" /></div>
  }
  return <WavePlayer ref={ref} url={signed.data.url} onTime={onTime} onLoadError={onLoadError} className={className} />
}
