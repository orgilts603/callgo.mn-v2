import { useCallback, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { ArrowRight, Clock, Download, Megaphone, PhoneForwarded, PhoneOff, User } from 'lucide-react'
import {
  AgentStateBadge, Button, Card, CallStatusBadge, Drawer, EmptyState, Skeleton,
} from '@/components/ui'
import { fmtDateTime, fmtPhone } from '@/lib/utils'
import type { Call, Contact } from '@/lib/types'
import { isLiveStatus, useCallDetail, useCampaignNames } from './api'
import { AudioPlayer, type AudioPlayerHandle } from './AudioPlayer'
import { CallDuration, DirectionIcon } from './callFormat'
import { HangupDialog, TransferDialog } from './CallActions'
import { SummaryCard } from './SummaryCard'
import { Transcript } from './Transcript'
import { useCallCacheSync } from './useCallCacheSync'
import { useCallEvents, type CallEventsState } from './useCallEvents'

export interface CallDrawerProps {
  /** Call to show; `null` closes the drawer. */
  callId: string | null
  onClose: () => void
}

function CallHeader({ call, contact, events }: { call?: Call; contact?: Contact | null; events: CallEventsState }) {
  const campaignNames = useCampaignNames(!!call?.campaignId)
  if (!call) return <Skeleton className="h-9 w-72" />
  const live = isLiveStatus(call.status)
  const contactName = contact?.name
  return (
    <div className="min-w-0 space-y-1">
      <div className="flex flex-wrap items-center gap-2">
        <DirectionIcon direction={call.direction} />
        <span className="flex items-center gap-1.5 truncate text-sm font-semibold text-[var(--fg)]">
          {fmtPhone(call.fromNumber)} <ArrowRight className="h-3.5 w-3.5 text-[var(--fg-subtle)]" /> {fmtPhone(call.toNumber)}
        </span>
        <CallStatusBadge status={call.status} />
        {live && events.agentState && <AgentStateBadge state={events.agentState} />}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--fg-muted)]">
        {contactName && <span className="inline-flex items-center gap-1"><User className="h-3 w-3" />{contactName}</span>}
        <span className="inline-flex items-center gap-1" title={fmtDateTime(call.startedAt)}>
          <Clock className="h-3 w-3" /><CallDuration call={call} />
        </span>
        {call.campaignId && (
          <Link to={`/campaigns/${call.campaignId}`} className="inline-flex items-center gap-1 text-[var(--accent-fg)] hover:underline">
            <Megaphone className="h-3 w-3" />{campaignNames.get(call.campaignId) ?? 'Кампанит ажил'}
          </Link>
        )}
        <span className="text-[var(--fg-subtle)]">{fmtDateTime(call.startedAt)}</span>
      </div>
    </div>
  )
}

function DrawerBody({ callId, events }: { callId: string; events: CallEventsState }) {
  const { data, isLoading, error } = useCallDetail(callId)
  const playerRef = useRef<AudioPlayerHandle>(null)
  const [playheadMs, setPlayheadMs] = useState<number | null>(null)
  const [confirmHangup, setConfirmHangup] = useState(false)
  const [transferOpen, setTransferOpen] = useState(false)
  const seek = useCallback((ms: number) => playerRef.current?.seekMs(ms), [])

  if (isLoading) {
    return (
      <div className="space-y-4 p-5">
        <Skeleton className="h-9 w-64" /><Skeleton className="h-20 w-full" /><Skeleton className="h-64 w-full" />
      </div>
    )
  }
  if (error || !data) {
    return <EmptyState title="Дуудлага олдсонгүй" description={error?.message} />
  }

  const { call } = data
  const live = isLiveStatus(call.status)
  const canHangup = call.status === 'active' || call.status === 'ringing'
  // Final turns are merged into the query cache by useCallCacheSync.
  const turns = data.turns
  const hasRecording = !!call.recordingUrl

  return (
    <div className="space-y-4 p-5">
      <div className="flex flex-wrap items-center gap-2">
        {canHangup && (
          <Button variant="danger" size="sm" onClick={() => setConfirmHangup(true)}>
            <PhoneOff className="h-3.5 w-3.5" /> Дуудлага таслах
          </Button>
        )}
        {canHangup && (
          <Button variant="secondary" size="sm" onClick={() => setTransferOpen(true)}>
            <PhoneForwarded className="h-3.5 w-3.5" /> Шилжүүлэх
          </Button>
        )}
        {hasRecording ? (
          <a href={call.recordingUrl} download target="_blank" rel="noreferrer"
            className="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--border)] px-3 text-xs font-medium text-[var(--fg)] hover:bg-[var(--surface-2)]">
            <Download className="h-3.5 w-3.5" /> Бичлэг татах
          </a>
        ) : (
          <Button variant="outline" size="sm" disabled title="Бичлэг байхгүй"><Download className="h-3.5 w-3.5" /> Бичлэг татах</Button>
        )}
      </div>

      <AudioPlayer ref={playerRef} url={call.recordingUrl} live={live} onTime={setPlayheadMs} />

      <SummaryCard call={call} liveModel={events.llmModel} />

      <Card className="overflow-hidden">
        <div className="flex items-center justify-between border-b border-[var(--border)] px-4 py-2.5">
          <h3 className="text-xs font-semibold text-[var(--fg)]">Яриа</h3>
          <span className="text-[11px] text-[var(--fg-subtle)]">Харилцагчийн үг дээр дарж засна</span>
        </div>
        <Transcript className="h-[52vh] min-h-72" callId={callId} turns={turns} partials={events.partials} live={live}
          activeMs={hasRecording ? playheadMs : null} onSeek={hasRecording ? seek : undefined} />
      </Card>

      {confirmHangup && <HangupDialog call={call} onClose={() => setConfirmHangup(false)} />}
      {transferOpen && <TransferDialog callId={call.id} onClose={() => setTransferOpen(false)} />}
    </div>
  )
}

/** Side drawer with the full call view: header, recording, summary, live transcript and actions. */
export function CallDrawer({ callId, onClose }: CallDrawerProps) {
  // Keep rendering the last call while the drawer slides out.
  const [shownId, setShownId] = useState<string | null>(callId)
  if (callId && callId !== shownId) setShownId(callId)
  const id = callId ?? shownId

  const { data } = useCallDetail(id)
  const events = useCallEvents(id)
  useCallCacheSync(id)

  return (
    <Drawer open={!!callId} onClose={onClose} width="max-w-3xl"
      header={id ? <CallHeader call={data?.call} contact={data?.contact} events={events} /> : null}>
      {id ? <DrawerBody key={id} callId={id} events={events} /> : null}
    </Drawer>
  )
}
