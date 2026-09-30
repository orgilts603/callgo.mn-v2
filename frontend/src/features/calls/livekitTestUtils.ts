// Test-only stand-in for the `livekit-client` module (jsdom has no WebRTC).
// Usage:  vi.mock('livekit-client', async () => (await import('./livekitTestUtils')).createLiveKitMock())
import { vi } from 'vitest'

type Handler = (...args: unknown[]) => void

export interface FakeParticipant { identity: string; name?: string; attributes?: Record<string, string>; kind?: number }

export class FakeRoom {
  static instances: FakeRoom[] = []
  /** When set, `connect()` rejects with it. */
  static connectError: Error | null = null
  handlers = new Map<string, Handler[]>()
  remoteParticipants = new Map<string, FakeParticipant>()
  localParticipant = { identity: 'op-u1', name: 'Бат Болд', attributes: { 'callgo.role': 'operator' }, kind: 0, publishTrack: vi.fn(async () => undefined) }
  connect = vi.fn(async (_url: string, _token: string) => { if (FakeRoom.connectError) throw FakeRoom.connectError })
  disconnect = vi.fn(async () => undefined)
  startAudio = vi.fn(async () => undefined)
  removeAllListeners = vi.fn(() => { this.handlers.clear() })
  constructor() { FakeRoom.instances.push(this) }
  on(event: string, fn: Handler) {
    this.handlers.set(event, [...(this.handlers.get(event) ?? []), fn])
    return this
  }
  emit(event: string, ...args: unknown[]) { this.handlers.get(event)?.forEach((fn) => fn(...args)) }
}

export function createLiveKitMock() {
  FakeRoom.instances = []
  FakeRoom.connectError = null
  const mic = {
    isMuted: false,
    stop: vi.fn(),
    mute: vi.fn(async function (this: { isMuted: boolean }) { mic.isMuted = true }),
    unmute: vi.fn(async function (this: { isMuted: boolean }) { mic.isMuted = false }),
  }
  return {
    Room: FakeRoom,
    RoomEvent: {
      ConnectionStateChanged: 'connectionStateChanged', Disconnected: 'disconnected', ParticipantConnected: 'participantConnected',
      ParticipantDisconnected: 'participantDisconnected', ActiveSpeakersChanged: 'activeSpeakersChanged', TrackSubscribed: 'trackSubscribed',
      TrackUnsubscribed: 'trackUnsubscribed', MediaDevicesError: 'mediaDevicesError',
    },
    ConnectionState: { Disconnected: 'disconnected', Connecting: 'connecting', Connected: 'connected', Reconnecting: 'reconnecting', SignalReconnecting: 'signalReconnecting' },
    ParticipantKind: { STANDARD: 0, INGRESS: 1, EGRESS: 2, SIP: 3, AGENT: 4 },
    Track: { Kind: { Audio: 'audio', Video: 'video' } },
    createLocalAudioTrack: vi.fn(async () => mic),
    __mic: mic,
  }
}

export type LiveKitMock = ReturnType<typeof createLiveKitMock>
