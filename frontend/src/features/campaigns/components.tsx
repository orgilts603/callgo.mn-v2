import { useState } from 'react'
import { Pause, Play, Trash2 } from 'lucide-react'
import { Button, Dialog } from '@/components/ui'
import type { Campaign } from '@/lib/types'
import { useCampaignAction, useDeleteCampaign } from './hooks'

export const canStart = (c: Campaign) => c.status === 'draft' || c.status === 'paused'
export const canPause = (c: Campaign) => c.status === 'running'
export const canDelete = (c: Campaign) => c.status === 'draft' || c.status === 'paused' || c.status === 'completed'

export function pct(part: number, total: number): number {
  return total > 0 ? Math.min(100, Math.round((part / total) * 100)) : 0
}

export function ProgressBar({ campaign }: { campaign: Campaign }) {
  const { total, completed, failed } = campaign
  const done = completed + failed
  const okPct = total > 0 ? (completed / total) * 100 : 0
  const failPct = total > 0 ? (failed / total) * 100 : 0
  return (
    <div className="min-w-40" title={`Дууссан ${completed} · Амжилтгүй ${failed} · Нийт ${total}`}>
      <div className="flex items-center justify-between text-xs tabular-nums">
        <span className="font-medium text-[var(--fg)]">{pct(done, total)}%</span>
        <span className="text-[var(--fg-muted)]">{done}/{total}</span>
      </div>
      <div role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct(done, total)}
        className="mt-1 flex h-1.5 overflow-hidden rounded-full bg-[var(--surface-3)]">
        <div className="h-full bg-emerald-500" style={{ width: `${okPct}%` }} />
        <div className="h-full bg-red-500" style={{ width: `${failPct}%` }} />
      </div>
      {failed > 0 && <div className="mt-0.5 text-[10px] text-red-300">{pct(failed, total)}% амжилтгүй</div>}
    </div>
  )
}

export function ConfirmDeleteDialog({ campaign, onClose, onDeleted }: { campaign: Campaign | null; onClose: () => void; onDeleted?: () => void }) {
  const del = useDeleteCampaign()
  return (
    <Dialog open={!!campaign} onClose={onClose} title="Кампанит ажил устгах"
      description="Энэ үйлдлийг буцаах боломжгүй."
      footer={<>
        <Button variant="ghost" onClick={onClose}>Болих</Button>
        <Button variant="danger" loading={del.isPending} onClick={() => {
          if (!campaign) return
          del.mutate(campaign.id, { onSuccess: () => { onClose(); onDeleted?.() } })
        }}>Устгах</Button>
      </>}>
      <p className="text-sm text-[var(--fg)]">«{campaign?.name}» кампанит ажил болон түүний бүх дугаарыг устгах уу?</p>
    </Dialog>
  )
}

export function CampaignActions({ campaign, size = 'sm', onDeleted }: { campaign: Campaign; size?: 'sm' | 'md'; onDeleted?: () => void }) {
  const action = useCampaignAction()
  const [confirm, setConfirm] = useState(false)
  return (
    <div className="flex items-center gap-1.5">
      {canStart(campaign) && (
        <Button size={size} variant="secondary" disabled={action.isPending} onClick={() => action.mutate({ id: campaign.id, action: 'start' })}>
          <Play className="h-3.5 w-3.5" />{campaign.status === 'paused' ? 'Үргэлжлүүлэх' : 'Эхлүүлэх'}
        </Button>
      )}
      {canPause(campaign) && (
        <Button size={size} variant="secondary" disabled={action.isPending} onClick={() => action.mutate({ id: campaign.id, action: 'pause' })}>
          <Pause className="h-3.5 w-3.5" />Түр зогсоох
        </Button>
      )}
      {canDelete(campaign) && (
        <Button size={size} variant="ghost" aria-label="Устгах" title="Устгах" onClick={() => setConfirm(true)}>
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      )}
      <ConfirmDeleteDialog campaign={confirm ? campaign : null} onClose={() => setConfirm(false)} onDeleted={onDeleted} />
    </div>
  )
}
