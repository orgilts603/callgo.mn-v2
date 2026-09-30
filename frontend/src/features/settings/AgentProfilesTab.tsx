import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { Bot, Mic, Pencil, Plus, Timer, Trash2, Volume2, Wand2 } from 'lucide-react'
import { Badge, Button, Card, CardBody, Drawer, EmptyState, Field, Input, Select, Skeleton, Textarea } from '@/components/ui'
import { fmtDuration } from '@/lib/utils'
import type { AgentProfile, LLMConfig } from '@/lib/types'
import { useAgentProfiles, useDeleteAgentProfile, useLLMConfigs, useSaveAgentProfile, type AgentProfileBody } from './hooks'
import { ConfirmDialog, ErrorNote, errMsg } from './common'

export const DEFAULT_PROMPT_TEMPLATE = `Та бол {{company}} компанийн дуут туслах юм. Таны нэр "Туяа".

Дүрэм:
- Үргэлж эелдэг, товч, ойлгомжтой монгол хэлээр ярь.
- Нэг удаад 1-2 өгүүлбэрээс илүүгүй хариул; утсаар ярьж байгаа тул урт жагсаалт бүү хэл.
- Тоо, огноо, үнийг тодорхой, аяар дуудаж хэл.
- Мэдэхгүй зүйлээ зохиохгүй; шаардлагатай бол хүнтэй холбоно гэж хэл.
- Хэрэглэгч яриагаа дуусгавал баярлалаа гэж хэлээд дуудлагыг өөрөө дуусга.`

export const TOOL_OPTIONS: { id: string; label: string; description: string }[] = [
  { id: 'end_call', label: 'end_call', description: 'Яриа дууссан үед дуудлагыг өөрөө таслах' },
  { id: 'transfer_call', label: 'transfer_call', description: 'Дуудлагыг оператор / бусад дугаар руу шилжүүлэх' },
  { id: 'lookup_contact', label: 'lookup_contact', description: 'Залгасан хүнийг харилцагчийн жагсаалтаас хайж мэдээлэл авах' },
  { id: 'schedule_callback', label: 'schedule_callback', description: 'Дараа буцаж залгахаар цаг товлох' },
]

const LANGUAGES = [{ value: 'mn', label: 'Монгол (mn)' }, { value: 'en', label: 'English (en)' }, { value: 'ru', label: 'Русский (ru)' }]
const STT_PROVIDERS = [
  { value: 'faster_whisper', label: 'faster-whisper (локал)', hint: 'Жишээ: large-v3, medium, small' },
  { value: 'openai', label: 'OpenAI', hint: 'Жишээ: gpt-4o-transcribe, whisper-1' },
  { value: 'google', label: 'Google', hint: 'Жишээ: chirp_2' },
  { value: 'groq', label: 'Groq', hint: 'Жишээ: whisper-large-v3-turbo' },
]
const TTS_PROVIDERS = [
  { value: 'piper', label: 'Piper (локал)', hint: 'Жишээ: mn_MN-mongolian-medium' },
  { value: 'openai', label: 'OpenAI', hint: 'Жишээ: alloy, nova, shimmer' },
  { value: 'google', label: 'Google', hint: 'Жишээ: mn-MN-Standard-A' },
]

function emptyBody(): AgentProfileBody {
  return {
    name: '', systemPrompt: '', greeting: 'Сайн байна уу, танд юугаар туслах вэ?', language: 'mn', llmConfigId: null,
    sttProvider: 'faster_whisper', sttModel: 'large-v3', ttsProvider: 'piper', ttsVoice: '', maxDurationSec: 600,
    tools: ['end_call'], transferNumber: '',
  }
}
function fromProfile(p: AgentProfile): AgentProfileBody {
  return {
    name: p.name, systemPrompt: p.systemPrompt, greeting: p.greeting, language: p.language || 'mn', llmConfigId: p.llmConfigId ?? null,
    sttProvider: p.sttProvider, sttModel: p.sttModel, ttsProvider: p.ttsProvider, ttsVoice: p.ttsVoice, maxDurationSec: p.maxDurationSec,
    tools: p.tools ?? [], transferNumber: p.transferNumber ?? '',
  }
}

export function AgentProfileEditor({ open, onClose, initial, llmConfigs }: { open: boolean; onClose: () => void; initial?: AgentProfile | null; llmConfigs: LLMConfig[] }) {
  return (
    <Drawer open={open} onClose={onClose} title={initial ? `Профайл засах: ${initial.name}` : 'Шинэ агент профайл'}>
      {/* Render the form only while open so state resets between uses. */}
      {open && <ProfileForm key={initial?.id ?? 'new'} initial={initial} llmConfigs={llmConfigs} onClose={onClose} />}
    </Drawer>
  )
}

function ProfileForm({ initial, llmConfigs, onClose }: { initial?: AgentProfile | null; llmConfigs: LLMConfig[]; onClose: () => void }) {
  const save = useSaveAgentProfile()
  const [f, setF] = useState<AgentProfileBody>(() => (initial ? fromProfile(initial) : emptyBody()))
  const [touched, setTouched] = useState(false)
  const set = <K extends keyof AgentProfileBody>(k: K, v: AgentProfileBody[K]) => setF((s) => ({ ...s, [k]: v }))
  const toggleTool = (id: string) => setF((s) => ({ ...s, tools: s.tools.includes(id) ? s.tools.filter((t) => t !== id) : [...s.tools, id] }))

  const sttHint = STT_PROVIDERS.find((p) => p.value === f.sttProvider)?.hint
  const ttsHint = TTS_PROVIDERS.find((p) => p.value === f.ttsProvider)?.hint
  const nameError = touched && !f.name.trim() ? 'Нэр оруулна уу' : null
  const transferError = touched && f.tools.includes('transfer_call') && !f.transferNumber?.trim() ? 'Шилжүүлэх дугаар оруулна уу' : null

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (!f.name.trim() || (f.tools.includes('transfer_call') && !f.transferNumber?.trim())) return
    const body: AgentProfileBody = { ...f, name: f.name.trim(), transferNumber: f.transferNumber?.trim() || undefined }
    save.mutate({ id: initial?.id, body }, {
      onSuccess: () => { toast.success(initial ? 'Профайл шинэчлэгдлээ' : 'Профайл үүслээ'); onClose() },
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <form onSubmit={submit} className="space-y-5 p-5" noValidate>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Нэр" error={nameError}><Input value={f.name} onChange={(e) => set('name', e.target.value)} placeholder="Борлуулалтын агент" /></Field>
        <Field label="Хэл"><Select value={f.language} onChange={(e) => set('language', e.target.value)} options={LANGUAGES} /></Field>
      </div>

      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <label htmlFor="agent-system-prompt" className="text-xs font-medium text-[var(--fg-muted)]">Системийн prompt</label>
          <Button type="button" size="sm" variant="outline" onClick={() => set('systemPrompt', DEFAULT_PROMPT_TEMPLATE)}><Wand2 className="h-3.5 w-3.5" />Монгол загвар оруулах</Button>
        </div>
        <Textarea id="agent-system-prompt" rows={12} className="min-h-56 font-mono text-[13px]" value={f.systemPrompt} onChange={(e) => set('systemPrompt', e.target.value)} placeholder="Агентын дүр, дүрэм, ярианы хэв маяг…" />
        <div className="text-right text-xs tabular-nums text-[var(--fg-subtle)]" data-testid="prompt-count">{f.systemPrompt.length} тэмдэгт</div>
      </div>

      <Field label="Мэндчилгээ" hint="Дуудлага эхлэхэд агентын хэлэх эхний өгүүлбэр">
        <Input value={f.greeting} onChange={(e) => set('greeting', e.target.value)} />
      </Field>

      <Field label="LLM тохиргоо" hint="«Үндсэн» бол байгууллагын default LLM ашиглана">
        <Select value={f.llmConfigId ?? ''} onChange={(e) => set('llmConfigId', e.target.value || null)} placeholder="Үндсэн (Default)"
          options={llmConfigs.map((c) => ({ value: c.id, label: `${c.name} · ${c.model}${c.isDefault ? ' ★' : ''}` }))} />
      </Field>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="STT провайдер"><Select value={f.sttProvider} onChange={(e) => set('sttProvider', e.target.value)} options={STT_PROVIDERS} /></Field>
        <Field label="STT загвар" hint={sttHint}><Input value={f.sttModel} onChange={(e) => set('sttModel', e.target.value)} /></Field>
        <Field label="TTS провайдер"><Select value={f.ttsProvider} onChange={(e) => set('ttsProvider', e.target.value)} options={TTS_PROVIDERS} /></Field>
        <Field label="TTS дуу хоолой" hint={ttsHint}><Input value={f.ttsVoice} onChange={(e) => set('ttsVoice', e.target.value)} /></Field>
      </div>

      <Field label="Дуудлагын дээд хугацаа (сек)" hint={`≈ ${fmtDuration(f.maxDurationSec)} минут`}>
        <Input type="number" min={30} step={30} value={f.maxDurationSec} onChange={(e) => set('maxDurationSec', Math.max(0, Number(e.target.value) || 0))} />
      </Field>

      <fieldset className="space-y-2">
        <legend className="text-xs font-medium text-[var(--fg-muted)]">Агентын хэрэгслүүд</legend>
        <div className="grid gap-2">
          {TOOL_OPTIONS.map((t) => (
            <label key={t.id} className="flex cursor-pointer items-start gap-3 rounded-md border border-[var(--border)] bg-[var(--surface-0)] px-3 py-2 hover:bg-[var(--surface-2)]">
              <input type="checkbox" className="mt-0.5 h-4 w-4 accent-[var(--accent)]" checked={f.tools.includes(t.id)} onChange={() => toggleTool(t.id)} />
              <span>
                <span className="block font-mono text-xs text-[var(--fg)]">{t.label}</span>
                <span className="block text-xs text-[var(--fg-muted)]">{t.description}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>

      <Field label="Шилжүүлэх дугаар" hint="transfer_call ашиглах үед дуудлага очих дугаар" error={transferError}>
        <Input value={f.transferNumber ?? ''} onChange={(e) => set('transferNumber', e.target.value)} placeholder="+97677001234" inputMode="tel" />
      </Field>

      <ErrorNote error={save.error} />
      <div className="sticky bottom-0 -mx-5 flex justify-end gap-2 border-t border-[var(--border)] bg-[var(--surface-1)] px-5 py-3">
        <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
        <Button type="submit" loading={save.isPending}>Хадгалах</Button>
      </div>
    </form>
  )
}

export default function AgentProfilesTab() {
  const profiles = useAgentProfiles()
  const llm = useLLMConfigs()
  const del = useDeleteAgentProfile()
  const [editing, setEditing] = useState<AgentProfile | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<AgentProfile | null>(null)
  const llmName = (id?: string | null) => llm.data?.find((c) => c.id === id)?.name
  const items = profiles.data ?? []

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <p className="max-w-3xl text-sm text-[var(--fg-muted)]">Агент профайл нь дуут агентын дүр, хэл, LLM, STT/TTS болон хэрэглэх хэрэгслүүдийг тодорхойлно. Профайлыг SIP дугаар болон кампанит ажилд оноож ашиглана.</p>
        <Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Профайл нэмэх</Button>
      </div>

      {profiles.isLoading ? (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3"><Skeleton className="h-44" /><Skeleton className="h-44" /><Skeleton className="h-44" /></div>
      ) : profiles.isError ? (
        <ErrorNote error={profiles.error} />
      ) : items.length === 0 ? (
        <Card><EmptyState icon={<Bot className="h-8 w-8" />} title="Агент профайл үүсээгүй байна" description="Эхний дуут агентаа тохируулна уу." action={<Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Профайл нэмэх</Button>} /></Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {items.map((p) => (
            <Card key={p.id} className="flex flex-col">
              <CardBody className="flex-1 space-y-3">
                <div className="flex items-start justify-between gap-2">
                  <div className="flex min-w-0 items-center gap-2">
                    <div className="grid h-8 w-8 shrink-0 place-items-center rounded-lg bg-[var(--accent)]/15 text-[var(--accent-fg)]"><Bot className="h-4 w-4" /></div>
                    <h3 className="truncate text-sm font-semibold text-[var(--fg)]">{p.name}</h3>
                  </div>
                  <Badge tone="accent">{p.language.toUpperCase()}</Badge>
                </div>
                <p className="line-clamp-3 min-h-12 text-xs leading-relaxed text-[var(--fg-muted)]">{p.systemPrompt || 'Prompt оруулаагүй'}</p>
                <div className="flex flex-wrap gap-1.5">
                  <Badge tone="neutral">LLM: {llmName(p.llmConfigId) ?? 'Default'}</Badge>
                  <Badge tone="neutral"><Mic className="h-3 w-3" />{p.sttProvider}</Badge>
                  <Badge tone="neutral"><Volume2 className="h-3 w-3" />{p.ttsProvider}</Badge>
                  <Badge tone="neutral"><Timer className="h-3 w-3" />{fmtDuration(p.maxDurationSec)}</Badge>
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {(p.tools ?? []).map((t) => <Badge key={t} tone="info" className="font-mono">{t}</Badge>)}
                </div>
              </CardBody>
              <div className="flex justify-end gap-1 border-t border-[var(--border)] px-3 py-2">
                <Button size="sm" variant="ghost" onClick={() => setEditing(p)}><Pencil className="h-3.5 w-3.5" />Засах</Button>
                <Button size="sm" variant="ghost" aria-label={`Устгах ${p.name}`} onClick={() => setDeleting(p)}><Trash2 className="h-3.5 w-3.5 text-red-400" />Устгах</Button>
              </div>
            </Card>
          ))}
        </div>
      )}

      <AgentProfileEditor open={creating || editing !== null} initial={editing} llmConfigs={llm.data ?? []} onClose={() => { setCreating(false); setEditing(null) }} />
      <ConfirmDialog
        open={deleting !== null} title="Профайл устгах уу?" description={deleting?.name} loading={del.isPending} onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting.id, { onSuccess: () => { toast.success('Устгагдлаа'); setDeleting(null) }, onError: (e) => toast.error(errMsg(e)) })}
      />
    </div>
  )
}
