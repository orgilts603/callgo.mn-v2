import { useCallback, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ArrowRight, Ban, Clock, Download, Megaphone, PhoneForwarded, PhoneOff, User } from 'lucide-react'
import {
  AgentStateBadge, Button, Card, CallStatusBadge, ConfirmDialog, Drawer, EmptyState, Field, Input, Skeleton,
} from '@/components/ui'
import { api } from '@/lib/api'
import { dncKey } from '@/features/settings/hooks'
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
      <div className="flex min-w-0 items-center gap-2 overflow-hidden">
        <DirectionIcon direction={call.direction} />
        <span className="flex shrink-0 items-center gap-1.5 text-sm font-semibold text-[var(--fg)]">
          {fmtPhone(call.fromNumber)} <ArrowRight className="h-3.5 w-3.5 text-[var(--fg-subtle)]" /> {fmtPhone(call.toNumber)}
        </span>
        <CallStatusBadge status={call.status} />
        {live && events.agentState && <AgentStateBadge state={events.agentState} />}
      </div>
      <div className="flex min-w-0 items-center gap-x-3 overflow-hidden whitespace-nowrap text-xs text-[var(--fg-muted)]">
        {contactName && <span className="inline-flex items-center gap-1"><User className="h-3 w-3" />{contactName}</span>}
        <span className="inline-flex items-center gap-1" title={fmtDateTime(call.startedAt)}>
          <Clock className="h-3 w-3" /><CallDuration call={call} />
        </span>
        {call.campaignId && (
          <Link to={`/campaigns/${call.campaignId}`} className="inline-flex items-center gap-1 text-[var(--accent-fg)] hover:underline">
            <Megaphone className="h-3 w-3" />{campaignNames.get(call.campaignId) ?? 'Кампанит ажил'}
          </Link>
        )}
        <span className="hidden text-[var(--fg-subtle)] sm:inline">{fmtDateTime(call.startedAt)}</span>
      </div>
    </div>
  )
}

/** The customer's number: the caller on inbound calls, the callee on outbound ones. */
export const customerNumber = (call: Pick<Call, 'direction' | 'fromNumber' | 'toNumber'>) =>
  (call.direction === 'inbound' ? call.fromNumber : call.toNumber) || ''

/** Confirm + POST /api/calls/{id}/dnc: adds the customer's number to the do-not-call list. */
function DncDialog({ call, onClose }: { call: Pick<Call, 'id' | 'direction' | 'fromNumber' | 'toNumber'>; onClose: () => void }) {
  const qc = useQueryClient()
  const [reason, setReason] = useState('')
  const number = customerNumber(call)
  const mutation = useMutation({
    mutationFn: () => api.post<unknown>(`/calls/${encodeURIComponent(call.id)}/dnc`, reason.trim() ? { reason: reason.trim() } : {}),
    onSuccess: () => {
      toast.success(`${fmtPhone(number)} хориглох жагсаалтад нэмэгдлээ`)
      void qc.invalidateQueries({ queryKey: dncKey })
    },
    onError: (err: Error) => { toast.error(err.message || 'Хориглох жагсаалтад нэмж чадсангүй') },
  })
  return (
    <ConfirmDialog open onClose={onClose} onConfirm={() => mutation.mutateAsync()} loading={mutation.isPending}
      title="Хориглох жагсаалтад нэмэх үү?" description={fmtPhone(number)} confirmLabel="Нэмэх">
      <p className="mb-3 text-sm text-[var(--fg-muted)]">Энэ дугаарт кампанит ажил болон гарах дуудлага цаашид хийгдэхгүй.</p>
      <Field label="Шалтгаан (заавал биш)">
        <Input value={reason} onChange={(e) => setReason(e.target.value)} aria-label="Шалтгаан" />
      </Field>
    </ConfirmDialog>
  )
}

function DrawerBody({ callId, events }: { callId: string; events: CallEventsState }) {
  const { data, isLoading, error } = useCallDetail(callId)
  const playerRef = useRef<AudioPlayerHandle>(null)
  const [playheadMs, setPlayheadMs] = useState<number | null>(null)
  const [confirmHangup, setConfirmHangup] = useState(false)
  const [transferOpen, setTransferOpen] = useState(false)
  const [dncOpen, setDncOpen] = useState(false)
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
        {customerNumber(call) && (
          <Button variant="outline" size="sm" onClick={() => setDncOpen(true)}>
            <Ban className="h-3.5 w-3.5" /> Хориглох жагсаалтад нэмэх
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
      {dncOpen && <DncDialog call={call} onClose={() => setDncOpen(false)} />}
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
