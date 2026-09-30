import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { AlertTriangle, Check, Copy, KeyRound, Plus, Trash2 } from 'lucide-react'
import {
  Badge, Button, Card, CardHeader, ConfirmDialog, Dialog, EmptyState, Field, Input, Skeleton, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import { HttpError } from '@/lib/api'
import type { APIKey } from '@/lib/types'
import { cn, fmtDateTime } from '@/lib/utils'
import { ErrorNote, errMsg } from './common'
import { API_SCOPES, maskedKey, scopeLabel } from './identity'
import { useAPIKeys, useCreateAPIKey, useRevokeAPIKey, type CreateAPIKeyResult } from './hooks'

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

function CreateDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  if (!open) return null
  return <CreateForm onClose={onClose} />
}

function CreateForm({ onClose }: { onClose: () => void }) {
  const create = useCreateAPIKey()
  const [name, setName] = useState('')
  const [scopes, setScopes] = useState<string[]>(['calls:read'])
  const [error, setError] = useState<string | null>(null)
  // The plaintext lives only in this component's state: closing the dialog discards it for good.
  const [created, setCreated] = useState<CreateAPIKeyResult | null>(null)
  const [copied, setCopied] = useState(false)
  const full = scopes.includes('*')

  const toggle = (s: string) => setScopes((cur) => (cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]))

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) { setError('Түлхүүрийн нэрийг оруулна уу.'); return }
    if (!scopes.length) { setError('Дор хаяж нэг эрх сонгоно уу.'); return }
    setError(null)
    create.mutate({ name: name.trim(), scopes: full ? ['*'] : scopes }, {
      onSuccess: (res) => setCreated(res),
      onError: (err) => setError(errMsg(err)),
    })
  }

  if (created) {
    return (
      <Dialog open onClose={onClose} title="API түлхүүр үүслээ" description={created.apiKey.name}
        footer={<Button onClick={onClose}>Хадгалсан, хаах</Button>}>
        <div className="space-y-3">
          <div role="alert" className="flex items-start gap-2 rounded-[var(--radius-sm)] border border-[var(--warning-border)] bg-[var(--warning-soft)] px-3 py-2 text-xs text-[var(--warning-fg)]">
            <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
            <span>Энэ түлхүүрийг <b>зөвхөн нэг удаа</b> харуулна. Одоо хуулж, найдвартай газар хадгална уу. Дахин харах боломжгүй.</span>
          </div>
          <div className="flex gap-2">
            <Input readOnly value={created.plaintext} aria-label="API түлхүүр" className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
            <Button type="button" variant="secondary" aria-label="Хуулах"
              onClick={async () => {
                if (await copyText(created.plaintext)) { setCopied(true); toast.success('Хуулагдлаа') }
                else toast.error('Хуулж чадсангүй. Гараар сонгож хуулна уу.')
              }}>
              {copied ? <Check /> : <Copy />} {copied ? 'Хуулсан' : 'Хуулах'}
            </Button>
          </div>
          <p className="text-xs text-[var(--fg-muted)]">
            Хүсэлт бүрт <code className="font-mono text-[var(--fg)]">Authorization: Bearer &lt;түлхүүр&gt;</code> header нэмнэ.
          </p>
        </div>
      </Dialog>
    )
  }

  return (
    <Dialog open onClose={onClose} title="API түлхүүр үүсгэх" description="Гадаад систем (CRM, ERP, скрипт) CallGo API-г байгууллагын нэрийн өмнөөс дуудахад ашиглана.">
      <form onSubmit={submit} noValidate className="space-y-4">
        <Field label="Нэр" hint="Жишээ: CRM интеграц">
          <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </Field>
        <fieldset className="space-y-1.5">
          <legend className="mb-1.5 text-xs font-medium text-[var(--fg-muted)]">Эрхүүд</legend>
          {API_SCOPES.map((s) => {
            const locked = full && s.value !== '*'
            return (
              <label key={s.value} className={cn('flex cursor-pointer items-start gap-2 rounded-[var(--radius-sm)] border border-[var(--border)] px-3 py-2', locked && 'cursor-not-allowed opacity-50')}>
                <input type="checkbox" className="mt-0.5 h-3.5 w-3.5 accent-[var(--accent)]" aria-label={s.label}
                  checked={locked || scopes.includes(s.value)} disabled={locked} onChange={() => toggle(s.value)} />
                <span className="min-w-0">
                  <span className="block text-[13px] text-[var(--fg)]">{s.label} <code className="ml-1 font-mono text-[11px] text-[var(--fg-subtle)]">{s.value}</code></span>
                  <span className="block text-xs text-[var(--fg-subtle)]">{s.hint}</span>
                </span>
              </label>
            )
          })}
        </fieldset>
        {error && <ErrorNote error={new Error(error)} />}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
          <Button type="submit" loading={create.isPending}>Үүсгэх</Button>
        </div>
      </form>
    </Dialog>
  )
}

export default function ApiKeysTab() {
  const q = useAPIKeys()
  const revoke = useRevokeAPIKey()
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<APIKey | null>(null)

  if (q.error instanceof HttpError && (q.error.code === 'feature_unavailable' || q.error.status === 403)) {
    const feature = q.error.code === 'feature_unavailable'
    return (
      <Card>
        <EmptyState icon={<KeyRound />}
          title={feature ? 'API түлхүүр таны багцад ороогүй' : 'Хандах эрхгүй'}
          description={feature ? 'API ашиглахын тулд багцаа ахиулна уу.' : 'API түлхүүрийг зөвхөн эзэмшигч болон админ удирдана.'}
          action={feature ? <Link to="/settings/billing" className="inline-flex h-7 items-center rounded-[var(--radius-sm)] border border-[var(--border)] bg-[var(--surface-2)] px-2.5 text-xs font-medium text-[var(--fg)] hover:bg-[var(--surface-3)]">Багц харах</Link> : undefined}
        />
      </Card>
    )
  }

  const keys = [...(q.data ?? [])].sort((a, b) => Number(!!a.revokedAt) - Number(!!b.revokedAt))

  return (
    <Card>
      <CardHeader title="API түлхүүрүүд" description="Сервер хоорондын интеграцид зориулсан түлхүүр. Түлхүүр бүр зөвхөн сонгосон эрхтэй."
        actions={<Button size="sm" onClick={() => setCreating(true)}><Plus /> Түлхүүр үүсгэх</Button>} />
      {q.isLoading ? (
        <div className="space-y-2 p-4"><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
      ) : q.isError ? (
        <div className="p-4"><ErrorNote error={q.error} /></div>
      ) : keys.length === 0 ? (
        <EmptyState icon={<KeyRound />} title="API түлхүүр алга" description="Гадаад системээс CallGo-г удирдах бол түлхүүр үүсгэнэ үү." />
      ) : (
        <Table>
          <THead><TR><TH>Нэр</TH><TH>Түлхүүр</TH><TH>Эрхүүд</TH><TH>Сүүлд ашигласан</TH><TH>Үүсгэсэн</TH><TH className="text-right"><span className="sr-only">Үйлдэл</span></TH></TR></THead>
          <TBody>
            {keys.map((k) => (
              <TR key={k.id} data-testid={`api-key-${k.id}`} className={cn(k.revokedAt && 'opacity-60')}>
                <TD className="font-medium">{k.name}</TD>
                <TD><code className="font-mono text-xs text-[var(--fg-muted)]">{maskedKey(k.prefix)}</code></TD>
                <TD>
                  <div className="flex flex-wrap gap-1">
                    {k.scopes.map((s) => <Badge key={s} tone="neutral" title={s}>{scopeLabel(s)}</Badge>)}
                  </div>
                </TD>
                <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{k.lastUsedAt ? fmtDateTime(k.lastUsedAt) : 'Ашиглаагүй'}</TD>
                <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtDateTime(k.createdAt)}</TD>
                <TD className="text-right">
                  {k.revokedAt ? <Badge tone="danger">Цуцлагдсан</Badge> : (
                    <Button size="sm" variant="ghost" className="hover:text-[var(--danger-fg)]" aria-label={`${k.name} цуцлах`} onClick={() => setRevoking(k)}>
                      <Trash2 /> Цуцлах
                    </Button>
                  )}
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}

      <CreateDialog open={creating} onClose={() => setCreating(false)} />
      <ConfirmDialog
        open={!!revoking} onClose={() => setRevoking(null)}
        title="API түлхүүр цуцлах"
        description={revoking ? `«${revoking.name}» түлхүүрээр хийгдэх хүсэлтүүд шууд татгалзагдана. Буцаах боломжгүй.` : undefined}
        confirmLabel="Цуцлах"
        onConfirm={async () => {
          if (!revoking) return
          try {
            await revoke.mutateAsync(revoking.id)
            toast.success('Түлхүүр цуцлагдлаа')
          } catch (err) {
            toast.error(errMsg(err))
            throw err
          }
        }}
      />
    </Card>
  )
}
