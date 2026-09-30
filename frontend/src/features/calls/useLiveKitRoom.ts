import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import {
  ConnectionState, createLocalAudioTrack, ParticipantKind, Room, RoomEvent, Track,
  type LocalAudioTrack, type Participant, type RemoteTrack,
} from 'livekit-client'

export type RoomPhase = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'disconnected' | 'error'
export type ParticipantRole = 'customer' | 'agent' | 'operator'

export interface RoomParticipant {
  identity: string
  name: string
  role: ParticipantRole
  speaking: boolean
  isLocal: boolean
}

export type RoomErrorKind = 'mic_denied' | 'mic_unavailable' | 'connect_failed'
export interface RoomError { kind: RoomErrorKind; message: string }

export interface JoinInfo { url: string; token: string }

export interface UseLiveKitRoom {
  phase: RoomPhase
  error: RoomError | null
  participants: RoomParticipant[]
  muted: boolean
  volume: number
  connect: (info: JoinInfo) => Promise<boolean>
  disconnect: () => Promise<void>
  toggleMute: () => void
  setVolume: (v: number) => void
}

export const MIC_DENIED_MESSAGE = 'Микрофонд хандах эрх олгогдоогүй байна. Хөтчийн тохиргоогоос микрофоныг зөвшөөрч, дахин оролдоно уу.'
export const MIC_UNAVAILABLE_MESSAGE = 'Микрофон олдсонгүй эсвэл ашиглах боломжгүй байна. Төхөөрөмжөө шалгана уу.'
export const CONNECT_FAILED_MESSAGE = 'Дуудлагын өрөөнд холбогдож чадсангүй. Сүлжээгээ шалгаад дахин оролдоно уу.'

/** Maps a getUserMedia failure to a Mongolian message. */
export function micError(err: unknown): RoomError {
  const name = err instanceof Error ? err.name : ''
  if (name === 'NotAllowedError' || name === 'SecurityError' || name === 'PermissionDeniedError') {
    return { kind: 'mic_denied', message: MIC_DENIED_MESSAGE }
  }
  return { kind: 'mic_unavailable', message: MIC_UNAVAILABLE_MESSAGE }
}

export function roleOf(p: Pick<Participant, 'identity' | 'attributes' | 'kind'>): ParticipantRole {
  const attr = p.attributes?.['callgo.role']
  if (attr === 'operator' || p.identity.startsWith('op-')) return 'operator'
  if (attr === 'agent' || p.kind === ParticipantKind.AGENT || p.identity.startsWith('agent')) return 'agent'
  return 'customer'
}

function snapshot(room: Room, speaking: ReadonlySet<string>): RoomParticipant[] {
  const all: Participant[] = [room.localParticipant, ...room.remoteParticipants.values()]
  return all.map((p) => ({
    identity: p.identity,
    name: p.name || p.identity,
    role: roleOf(p),
    speaking: speaking.has(p.identity),
    isLocal: p === room.localParticipant,
  }))
}

/**
 * Joins a LiveKit room as an operator: publishes the microphone, plays every
 * remote audio track into `audioContainer` and exposes participants, speaking
 * state, connection phase, mute and volume.
 *
 * `connect` asks for the microphone first (so a denied permission never leaves
 * a silent operator in the room), then connects, then publishes. It resolves
 * `true` on success and `false` after setting `error`.
 */
export function useLiveKitRoom(audioContainer: RefObject<HTMLElement | null>): UseLiveKitRoom {
  const roomRef = useRef<Room | null>(null)
  const micRef = useRef<LocalAudioTrack | null>(null)
  const speakingRef = useRef<Set<string>>(new Set())
  const elementsRef = useRef<Set<HTMLMediaElement>>(new Set())
  const volumeRef = useRef(1)
  const [phase, setPhase] = useState<RoomPhase>('idle')
  const [error, setError] = useState<RoomError | null>(null)
  const [participants, setParticipants] = useState<RoomParticipant[]>([])
  const [muted, setMuted] = useState(false)
  const [volume, setVolumeState] = useState(1)

  const cleanup = useCallback(async () => {
    const room = roomRef.current
    roomRef.current = null
    const mic = micRef.current
    micRef.current = null
    speakingRef.current = new Set()
    for (const el of elementsRef.current) el.remove()
    elementsRef.current.clear()
    mic?.stop()
    if (room) {
      room.removeAllListeners()
      await room.disconnect()
    }
  }, [])

  const connect = useCallback(async ({ url, token }: JoinInfo): Promise<boolean> => {
    await cleanup()
    setError(null); setMuted(false); setParticipants([]); setPhase('connecting')

    let mic: LocalAudioTrack
    try {
      mic = await createLocalAudioTrack({ echoCancellation: true, noiseSuppression: true, autoGainControl: true })
    } catch (err) {
      setError(micError(err)); setPhase('error')
      return false
    }

    const room = new Room()
    roomRef.current = room
    micRef.current = mic
    const refresh = () => { if (roomRef.current === room) setParticipants(snapshot(room, speakingRef.current)) }

    room
      .on(RoomEvent.ConnectionStateChanged, (state: ConnectionState) => {
        if (roomRef.current !== room) return
        if (state === ConnectionState.Connected) setPhase('connected')
        else if (state === ConnectionState.Reconnecting || state === ConnectionState.SignalReconnecting) setPhase('reconnecting')
        else if (state === ConnectionState.Connecting) setPhase('connecting')
        else if (state === ConnectionState.Disconnected) setPhase('disconnected')
      })
      .on(RoomEvent.Disconnected, () => { if (roomRef.current === room) setPhase('disconnected') })
      .on(RoomEvent.ParticipantConnected, refresh)
      .on(RoomEvent.ParticipantDisconnected, refresh)
      .on(RoomEvent.ActiveSpeakersChanged, (speakers: Participant[]) => {
        speakingRef.current = new Set(speakers.map((s) => s.identity))
        refresh()
      })
      .on(RoomEvent.TrackSubscribed, (track: RemoteTrack) => {
        if (track.kind !== Track.Kind.Audio) return
        const el = track.attach()
        el.volume = volumeRef.current
        el.setAttribute('data-livekit-audio', '')
        elementsRef.current.add(el)
        audioContainer.current?.appendChild(el)
      })
      .on(RoomEvent.TrackUnsubscribed, (track: RemoteTrack) => {
        for (const el of track.detach()) { elementsRef.current.delete(el); el.remove() }
      })
      .on(RoomEvent.MediaDevicesError, (err: Error) => {
        if (roomRef.current !== room) return
        setError(micError(err))
      })

    try {
      await room.connect(url, token)
      await room.localParticipant.publishTrack(mic)
    } catch {
      if (roomRef.current === room) {
        await cleanup()
        setError({ kind: 'connect_failed', message: CONNECT_FAILED_MESSAGE }); setPhase('error')
      }
      return false
    }
    if (roomRef.current !== room) return false
    // Browsers may block autoplay until a gesture; connect() runs inside one.
    void room.startAudio().catch(() => undefined)
    setPhase('connected')
    refresh()
    return true
  }, [audioContainer, cleanup])

  const disconnect = useCallback(async () => {
    await cleanup()
    setParticipants([])
    setPhase((p) => (p === 'idle' || p === 'error' ? p : 'disconnected'))
  }, [cleanup])

  const toggleMute = useCallback(() => {
    const mic = micRef.current
    if (!mic) return
    if (mic.isMuted) { void mic.unmute(); setMuted(false) } else { void mic.mute(); setMuted(true) }
  }, [])

  const setVolume = useCallback((v: number) => {
    const clamped = Math.min(1, Math.max(0, v))
    volumeRef.current = clamped
    for (const el of elementsRef.current) el.volume = clamped
    setVolumeState(clamped)
  }, [])

  // Leave the room when the console unmounts.
  useEffect(() => () => { void cleanup() }, [cleanup])

  return useMemo(
    () => ({ phase, error, participants, muted, volume, connect, disconnect, toggleMute, setVolume }),
    [phase, error, participants, muted, volume, connect, disconnect, toggleMute, setVolume],
  )
}
