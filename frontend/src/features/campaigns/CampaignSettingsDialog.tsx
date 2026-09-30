import { useState } from 'react'
import { Button, Dialog, Field, Input, Textarea } from '@/components/ui'
import type { Campaign } from '@/lib/types'
import { OutcomesEditor, ScheduleEditor } from './editors'
import { useUpdateCampaign, type CampaignUpdateBody } from './hooks'
import {
  draftFromOutcomes, draftFromSchedule, outcomesToJSON, outcomesValid, scheduleToJSON, validateSchedule,
} from './schedule'

const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, Math.round(n) || lo))

/** Only draft/paused/running campaigns are editable; while running only schedule/concurrency/outcomes may change. */
export const canEditSettings = (c: Campaign) => c.status !== 'completed'

export function buildUpdateBody(
  c: Campaign,
  v: { schedule: ReturnType<typeof draftFromSchedule>; outcomes: ReturnType<typeof draftFromOutcomes>; concurrency: number; maxAttempts: number; script: string },
): CampaignUpdateBody {
  const body: CampaignUpdateBody = { schedule: scheduleToJSON(v.schedule), outcomes: outcomesToJSON(v.outcomes), concurrency: v.concurrency }
  if (c.status !== 'running') { body.maxAttempts = v.maxAttempts; body.script = v.script }
  return body
}

function SettingsForm({ campaign, onClose }: { campaign: Campaign; onClose: () => void }) {
  const update = useUpdateCampaign()
  const running = campaign.status === 'running'
  const [schedule, setSchedule] = useState(() => draftFromSchedule(campaign.schedule))
  const [outcomes, setOutcomes] = useState(() => draftFromOutcomes(campaign.outcomes))
  const [concurrency, setConcurrency] = useState(campaign.concurrency)
  const [maxAttempts, setMaxAttempts] = useState(campaign.maxAttempts)
  const [script, setScript] = useState(campaign.script)
  const valid = !validateSchedule(schedule) && outcomesValid(outcomes)

  const save = () => {
    update.mutate(
      { id: campaign.id, body: buildUpdateBody(campaign, { schedule, outcomes, concurrency, maxAttempts, script }) },
      { onSuccess: onClose },
    )
  }

  return (
    <Dialog open onClose={onClose} title="Тохиргоо засах" className="max-w-2xl"
      description={running ? 'Ажиллаж байх үед зөвхөн хуваарь, зэрэг дуудлага, үр дүнгийн ангилал өөрчлөгдөнө.' : undefined}
      footer={<>
        <Button variant="ghost" onClick={onClose}>Болих</Button>
        <Button disabled={!valid} loading={update.isPending} onClick={save}>Хадгалах</Button>
      </>}>
      <div className="max-h-[60vh] space-y-6 overflow-y-auto pr-1">
        <section className="space-y-3">
          <h3 className="text-xs font-semibold text-[var(--fg)]">Ерөнхий</h3>
          <Field label="Скрипт">
            <Textarea rows={4} value={script} disabled={running} onChange={(e) => setScript(e.target.value)} />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Зэрэг дуудлага (1-50)">
              <Input type="number" min={1} max={50} value={concurrency} onChange={(e) => setConcurrency(clamp(Number(e.target.value), 1, 50))} />
            </Field>
            <Field label="Дахин оролдох тоо (1-5)">
              <Input type="number" min={1} max={5} value={maxAttempts} disabled={running} onChange={(e) => setMaxAttempts(clamp(Number(e.target.value), 1, 5))} />
            </Field>
          </div>
        </section>
        <section className="space-y-3">
          <h3 className="text-xs font-semibold text-[var(--fg)]">Хуваарь</h3>
          <ScheduleEditor value={schedule} onChange={setSchedule} />
        </section>
        <section className="space-y-3">
          <h3 className="text-xs font-semibold text-[var(--fg)]">Үр дүнгийн ангилал</h3>
          <OutcomesEditor value={outcomes} onChange={setOutcomes} />
        </section>
      </div>
    </Dialog>
  )
}

/** Remounts the form on every open so state re-initialises from the latest campaign. */
export function CampaignSettingsDialog({ campaign, open, onClose }: { campaign: Campaign; open: boolean; onClose: () => void }) {
  if (!open) return null
  return <SettingsForm campaign={campaign} onClose={onClose} />
}
