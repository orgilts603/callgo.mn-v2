import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { ArrowRight, FlaskConical, Pencil, Plus, Star, Thermometer, Trash2, Zap } from 'lucide-react'
import { Badge, Button, Card, CardBody, Dialog, EmptyState, Field, Input, Select, Skeleton } from '@/components/ui'
import { cn } from '@/lib/utils'
import type { LLMCatalogEntry, LLMConfig, LLMProvider } from '@/lib/types'
import {
  useDeleteLLMConfig, useLLMCatalog, useLLMConfigs, useSaveLLMConfig, useTestLLMConfig, type LLMConfigBody, type LLMTestResult,
} from './hooks'
import { ConfirmDialog, ErrorNote, SwitchRow, errMsg } from './common'

const FALLBACK_PROVIDERS: LLMCatalogEntry[] = [
  { provider: 'openai', label: 'OpenAI', models: [], needsApiKey: true, needsBaseUrl: false },
  { provider: 'anthropic', label: 'Anthropic', models: [], needsApiKey: true, needsBaseUrl: false },
  { provider: 'google', label: 'Google', models: [], needsApiKey: true, needsBaseUrl: false },
  { provider: 'groq', label: 'Groq', models: [], needsApiKey: true, needsBaseUrl: false },
  { provider: 'ollama', label: 'Ollama', models: [], needsApiKey: false, needsBaseUrl: true },
  { provider: 'openai_compatible', label: 'OpenAI-compatible', models: [], needsApiKey: true, needsBaseUrl: true },
]

const providerStyle: Record<LLMProvider, { initial: string; cls: string }> = {
  openai: { initial: 'O', cls: 'bg-emerald-500/15 text-emerald-300' },
  anthropic: { initial: 'A', cls: 'bg-orange-500/15 text-orange-300' },
  google: { initial: 'G', cls: 'bg-white/[0.06] text-zinc-200' },
  groq: { initial: 'Q', cls: 'bg-fuchsia-500/15 text-fuchsia-300' },
  ollama: { initial: 'L', cls: 'bg-zinc-500/20 text-zinc-200' },
  openai_compatible: { initial: '⚙', cls: 'bg-white/[0.06] text-zinc-300' },
}
function ProviderLogo({ provider, className }: { provider: LLMProvider; className?: string }) {
  const s = providerStyle[provider] ?? { initial: '?', cls: 'bg-[var(--surface-2)]' }
  return <div className={cn('grid h-9 w-9 shrink-0 place-items-center rounded-lg text-sm font-bold', s.cls, className)} aria-hidden>{s.initial}</div>
}

/** Whether the base URL field applies to the given provider. */
export function needsBaseUrl(provider: LLMProvider, entry?: LLMCatalogEntry): boolean {
  return Boolean(entry?.needsBaseUrl) || provider === 'openai_compatible' || provider === 'ollama'
}

export function LLMTestButton({ config, size = 'sm' }: { config: LLMConfig; size?: 'sm' | 'md' }) {
  const test = useTestLLMConfig()
  const [result, setResult] = useState<LLMTestResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  function run() {
    setResult(null); setError(null)
    test.mutate({ id: config.id, prompt: 'Сайн байна уу! Нэг өгүүлбэрээр өөрийгөө танилцуул.' }, {
      onSuccess: (r) => setResult(r), onError: (e) => setError(errMsg(e)),
    })
  }
  return (
    <div className="space-y-2">
      <Button type="button" size={size} variant="outline" loading={test.isPending} onClick={run}><FlaskConical className="h-3.5 w-3.5" />Тест</Button>
      {result && result.ok && (
        <div role="status" className="rounded-md border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-200">
          <div className="mb-1 flex items-center gap-1.5 font-medium"><Zap className="h-3 w-3" />{result.latencyMs} мс</div>
          <div className="whitespace-pre-wrap text-[var(--fg)]">{result.reply}</div>
        </div>
      )}
      {result && !result.ok && <div role="alert" className="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">Алдаа: {result.error || 'Тодорхойгүй алдаа'}</div>}
      {error && <div role="alert" className="rounded-md border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-300">Алдаа: {error}</div>}
    </div>
  )
}

export function LLMConfigDialog({ open, onClose, initial, catalog, configs }: {
  open: boolean; onClose: () => void; initial?: LLMConfig | null; catalog: LLMCatalogEntry[]; configs: LLMConfig[]
}) {
  return (
    <Dialog open={open} onClose={onClose} className="max-w-xl" title={initial ? `LLM засах: ${initial.name}` : 'LLM тохиргоо нэмэх'}
      description="API түлхүүр нууцлагдмал хадгалагдах бөгөөд дахин харагдахгүй.">
      <LLMConfigForm key={initial?.id ?? 'new'} initial={initial} catalog={catalog} configs={configs} onClose={onClose} />
    </Dialog>
  )
}

function LLMConfigForm({ initial, catalog, configs, onClose }: { initial?: LLMConfig | null; catalog: LLMCatalogEntry[]; configs: LLMConfig[]; onClose: () => void }) {
  const save = useSaveLLMConfig()
  const providers = catalog.length > 0 ? catalog : FALLBACK_PROVIDERS
  const [name, setName] = useState(initial?.name ?? '')
  const [provider, setProvider] = useState<LLMProvider>(initial?.provider ?? providers[0]?.provider ?? 'openai')
  const [model, setModel] = useState(initial?.model ?? providers[0]?.models[0] ?? '')
  const [baseUrl, setBaseUrl] = useState(initial?.baseUrl ?? '')
  const [apiKey, setApiKey] = useState('')
  const [temperature, setTemperature] = useState(initial?.temperature ?? 0.7)
  const [maxTokens, setMaxTokens] = useState(initial?.maxTokens ?? 1024)
  const [isDefault, setIsDefault] = useState(initial?.isDefault ?? false)
  const [fallbackId, setFallbackId] = useState(initial?.fallbackId ?? '')
  const [touched, setTouched] = useState(false)

  const entry = providers.find((p) => p.provider === provider)
  const showBaseUrl = needsBaseUrl(provider, entry)
  const requireKey = Boolean(entry?.needsApiKey) && !initial
  const errors = {
    name: !name.trim() ? 'Нэр оруулна уу' : null,
    model: !model.trim() ? 'Загвар оруулна уу' : null,
    baseUrl: showBaseUrl && !baseUrl.trim() ? 'Base URL шаардлагатай' : null,
    apiKey: requireKey && !apiKey.trim() ? 'API түлхүүр шаардлагатай' : null,
  }
  const hasError = Object.values(errors).some(Boolean)

  function changeProvider(next: LLMProvider) {
    const nextEntry = providers.find((p) => p.provider === next)
    setProvider(next)
    // Swap to the new provider's first model unless the user typed a custom one.
    const knownModels = providers.flatMap((p) => p.models)
    if (!model || knownModels.includes(model)) setModel(nextEntry?.models[0] ?? '')
  }

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (hasError) return
    const body: LLMConfigBody = {
      name: name.trim(), provider, model: model.trim(), ...(showBaseUrl ? { baseUrl: baseUrl.trim() } : {}),
      apiKey: apiKey.trim(), temperature, maxTokens, isDefault, fallbackId: fallbackId || null,
    }
    save.mutate({ id: initial?.id, body }, {
      onSuccess: () => { toast.success(initial ? 'Тохиргоо шинэчлэгдлээ' : 'Тохиргоо нэмэгдлээ'); onClose() },
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  const err = (k: keyof typeof errors) => (touched ? errors[k] : null)
  const listId = 'llm-model-options'

  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      <Field label="Нэр" error={err('name')}><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Үндсэн GPT" autoFocus /></Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Провайдер">
          <Select value={provider} onChange={(e) => changeProvider(e.target.value as LLMProvider)} options={providers.map((p) => ({ value: p.provider, label: p.label }))} />
        </Field>
        <Field label="Загвар" error={err('model')} hint={entry && entry.models.length === 0 ? 'Загварын нэрийг гараар оруулна' : undefined}>
          <Input value={model} onChange={(e) => setModel(e.target.value)} list={listId} placeholder="gpt-4o-mini" />
          <datalist id={listId}>{entry?.models.map((m) => <option key={m} value={m} />)}</datalist>
        </Field>
      </div>
      <div className="flex flex-wrap gap-1.5 text-[11px]">
        <Badge tone={entry?.needsApiKey ? 'warning' : 'neutral'}>{entry?.needsApiKey ? 'API түлхүүр шаардлагатай' : 'API түлхүүр шаардлагагүй'}</Badge>
        {showBaseUrl && <Badge tone="info">Base URL шаардлагатай</Badge>}
      </div>
      {showBaseUrl && (
        <Field label="Base URL" error={err('baseUrl')} hint={provider === 'ollama' ? 'Жишээ: http://localhost:11434' : 'Жишээ: https://api.example.com/v1'}>
          <Input value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder={provider === 'ollama' ? 'http://localhost:11434' : 'https://api.example.com/v1'} />
        </Field>
      )}
      <Field label="API түлхүүр" error={err('apiKey')} hint={initial ? 'Хоосон орхивол одоогийн түлхүүр хэвээр үлдэнэ' : undefined}>
        <Input type="password" autoComplete="new-password" value={apiKey} onChange={(e) => setApiKey(e.target.value)}
          placeholder={initial?.apiKeyHint ? `Одоогийн: ${initial.apiKeyHint}` : 'sk-…'} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={`Temperature: ${temperature.toFixed(1)}`} hint="0 = тогтвортой, 2 = маш санамсаргүй">
          <input type="range" min={0} max={2} step={0.1} value={temperature} onChange={(e) => setTemperature(Number(e.target.value))} className="h-9 w-full accent-[var(--accent)]" />
        </Field>
        <Field label="Max tokens"><Input type="number" min={1} value={maxTokens} onChange={(e) => setMaxTokens(Number(e.target.value) || 0)} /></Field>
      </div>
      <Field label="Fallback" hint="Энэ тохиргоо алдаа өгвөл дараа нь ашиглах тохиргоо">
        <Select value={fallbackId} onChange={(e) => setFallbackId(e.target.value)} placeholder="— Байхгүй —"
          options={configs.filter((c) => c.id !== initial?.id).map((c) => ({ value: c.id, label: `${c.name} · ${c.model}` }))} />
      </Field>
      <SwitchRow label="Үндсэн (default) болгох" checked={isDefault} onChange={setIsDefault} />
      {initial && <LLMTestButton config={initial} size="md" />}
      <ErrorNote error={save.error} />
      <div className="flex justify-end gap-2 pt-1">
        <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
        <Button type="submit" loading={save.isPending}>Хадгалах</Button>
      </div>
    </form>
  )
}

export default function LLMConfigsTab() {
  const configs = useLLMConfigs()
  const catalog = useLLMCatalog()
  const del = useDeleteLLMConfig()
  const [editing, setEditing] = useState<LLMConfig | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<LLMConfig | null>(null)
  const items = configs.data ?? []
  const byId = (id?: string | null) => items.find((c) => c.id === id)
  const providerLabel = (p: LLMProvider) => catalog.data?.find((c) => c.provider === p)?.label ?? p

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <p className="max-w-3xl text-sm text-[var(--fg-muted)]">Дуудлага бүр LLM тохиргоогоор дамжина. Fallback гинж тохируулбал үндсэн загвар алдаа өгөхөд дараагийнх нь автоматаар ажиллана.</p>
        <Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />LLM нэмэх</Button>
      </div>

      {configs.isLoading ? (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3"><Skeleton className="h-48" /><Skeleton className="h-48" /></div>
      ) : configs.isError ? (
        <ErrorNote error={configs.error} />
      ) : items.length === 0 ? (
        <Card><EmptyState icon={<Zap className="h-8 w-8" />} title="LLM тохиргоо байхгүй байна" description="Provider болон API түлхүүрээ оруулж эхний тохиргоогоо үүсгэнэ үү." action={<Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />LLM нэмэх</Button>} /></Card>
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {items.map((c) => {
            const fb = byId(c.fallbackId)
            return (
              <Card key={c.id} className="flex flex-col">
                <CardBody className="flex-1 space-y-3">
                  <div className="flex items-start gap-3">
                    <ProviderLogo provider={c.provider} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <h3 className="truncate text-sm font-semibold text-[var(--fg)]">{c.name}</h3>
                        {c.isDefault && <Star className="h-3.5 w-3.5 shrink-0 fill-amber-400 text-amber-400" aria-label="Үндсэн" />}
                      </div>
                      <div className="text-xs text-[var(--fg-muted)]">{providerLabel(c.provider)}</div>
                    </div>
                  </div>
                  <div className="font-mono text-xs text-[var(--fg)]">{c.model}</div>
                  <div className="flex flex-wrap gap-1.5">
                    <Badge tone="neutral"><Thermometer className="h-3 w-3" />{c.temperature}</Badge>
                    <Badge tone="neutral">{c.maxTokens} tok</Badge>
                    <Badge tone="neutral" className="font-mono">{c.apiKeyHint ? `key ${c.apiKeyHint}` : 'key —'}</Badge>
                  </div>
                  {fb && (
                    <div className="flex items-center gap-1.5 text-xs text-[var(--fg-muted)]"><span>{c.name}</span><ArrowRight className="h-3 w-3" /><span className="font-medium text-[var(--fg)]">{fb.name}</span></div>
                  )}
                  <LLMTestButton config={c} />
                </CardBody>
                <div className="flex justify-end gap-1 border-t border-[var(--border)] px-3 py-2">
                  <Button size="sm" variant="ghost" onClick={() => setEditing(c)}><Pencil className="h-3.5 w-3.5" />Засах</Button>
                  <Button size="sm" variant="ghost" aria-label={`Устгах ${c.name}`} onClick={() => setDeleting(c)}><Trash2 className="h-3.5 w-3.5 text-red-400" />Устгах</Button>
                </div>
              </Card>
            )
          })}
        </div>
      )}

      <LLMConfigDialog open={creating || editing !== null} initial={editing} catalog={catalog.data ?? []} configs={items} onClose={() => { setCreating(false); setEditing(null) }} />
      <ConfirmDialog
        open={deleting !== null} title="LLM тохиргоо устгах уу?" description={deleting?.name} loading={del.isPending} onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting.id, { onSuccess: () => { toast.success('Устгагдлаа'); setDeleting(null) }, onError: (e) => toast.error(errMsg(e)) })}
      />
    </div>
  )
}
