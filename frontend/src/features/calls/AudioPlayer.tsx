import { useEffect, useImperativeHandle, useRef, useState, type Ref } from 'react'
import WaveSurfer from 'wavesurfer.js'
import { AlertCircle, Pause, Play, VolumeX } from 'lucide-react'
import { Button, Skeleton } from '@/components/ui'
import { cn, fmtDuration } from '@/lib/utils'

export interface AudioPlayerHandle {
  /** Seek to a position in milliseconds and start playback. */
  seekMs: (ms: number) => void
}

export interface AudioPlayerProps {
  url?: string | null
  /** Shown in the empty state when there is no recording yet. */
  live?: boolean
  /** Playback position callback (throttled to ~4 updates per second). */
  onTime?: (ms: number) => void
  ref?: Ref<AudioPlayerHandle>
  className?: string
}

export const PLAYBACK_RATES = [1, 1.5, 2] as const

function cssVar(name: string, fallback: string): string {
  if (typeof window === 'undefined') return fallback
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

/** Waveform player for a call recording (wavesurfer.js). */
export function AudioPlayer({ url, live, onTime, ref, className }: AudioPlayerProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const wsRef = useRef<WaveSurfer | null>(null)
  const pendingSeek = useRef<number | null>(null)
  const onTimeRef = useRef(onTime)
  const [ready, setReady] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [playing, setPlaying] = useState(false)
  const [duration, setDuration] = useState(0)
  const [current, setCurrent] = useState(0)
  const [rate, setRate] = useState<number>(1)

  useEffect(() => { onTimeRef.current = onTime }, [onTime])

  useEffect(() => {
    const container = containerRef.current
    if (!url || !container) return
    setReady(false); setError(null); setPlaying(false); setCurrent(0); setDuration(0)
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
      setCurrent(t)
      onTimeRef.current?.(Math.round(t * 1000))
    }
    const subs = [
      ws.on('ready', (d) => {
        setDuration(d); setReady(true)
        if (pendingSeek.current != null) { ws.setTime(pendingSeek.current / 1000); pendingSeek.current = null; void ws.play() }
      }),
      ws.on('timeupdate', emitTime),
      ws.on('play', () => setPlaying(true)),
      ws.on('pause', () => setPlaying(false)),
      ws.on('finish', () => setPlaying(false)),
      ws.on('error', (e) => setError(e?.message || 'Бичлэг ачаалж чадсангүй')),
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

  if (!url) {
    return (
      <div className={cn('flex items-center gap-3 rounded-lg border border-dashed border-[var(--border)] px-4 py-3 text-xs text-[var(--fg-muted)]', className)} data-testid="audio-empty">
        <VolumeX className="h-4 w-4 text-[var(--fg-subtle)]" />
        {live ? 'Дуудлага дууссаны дараа бичлэг гарна.' : 'Энэ дуудлагад бичлэг байхгүй.'}
      </div>
    )
  }

  return (
    <div className={cn('rounded-lg border border-[var(--border)] bg-[var(--surface-0)] p-3', className)}>
      <div className="flex items-center gap-3">
        <Button size="icon" variant="primary" className="h-9 w-9 shrink-0 rounded-full" disabled={!ready}
          onClick={() => void wsRef.current?.playPause()} aria-label={playing ? 'Түр зогсоох' : 'Тоглуулах'}>
          {playing ? <Pause className="h-4 w-4" /> : <Play className="h-4 w-4 translate-x-px" />}
        </Button>
        <div className="relative min-w-0 flex-1">
          <div ref={containerRef} className={cn('w-full', !ready && !error && 'opacity-0')} data-testid="waveform" />
          {!ready && !error && <Skeleton className="absolute inset-0 h-14" />}
          {error && (
            <div className="absolute inset-0 flex items-center gap-2 text-xs text-red-300">
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
                rate === r ? 'bg-[var(--accent)]/20 text-[var(--accent-fg)]' : 'hover:bg-[var(--surface-2)] hover:text-[var(--fg)]')}>
              {r}x
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
