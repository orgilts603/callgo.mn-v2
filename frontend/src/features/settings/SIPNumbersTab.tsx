import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { ArrowDownLeft, ArrowUpRight, Check, Pencil, Phone, Plus, RefreshCw, Trash2 } from 'lucide-react'
import {
  Badge, Button, Card, CardBody, Dialog, EmptyState, Field, Input, Select, Skeleton, Table, TBody, TD, TH, THead, TR,
} from '@/components/ui'
import { fmtPhone } from '@/lib/utils'
import type { AgentProfile, SIPNumber } from '@/lib/types'
import { useAgentProfiles, useDeleteSIPNumber, useProvisionSIPNumber, useSaveSIPNumber, useSIPNumbers, type SIPNumberBody } from './hooks'
import { ConfirmDialog, ErrorNote, SwitchRow, errMsg } from './common'

const E164 = /^\+[1-9]\d{6,14}$/

/** Normalises user input: strips spaces/dashes; a bare 8-digit Mongolian number gets +976. */
export function normalizeNumber(raw: string): string {
  const s = raw.replace(/[\s\-()]/g, '')
  return /^\d{8}$/.test(s) ? `+976${s}` : s
}
export function validateNumber(raw: string): string | null {
  const n = normalizeNumber(raw)
  if (!n) return 'Дугаар оруулна уу'
  if (!E164.test(n)) return 'E.164 форматтай байх ёстой (жишээ: +97670001234)'
  return null
}

const short = (id?: string) => (id ? (id.length > 10 ? `${id.slice(0, 8)}…` : id) : '—')
export const isProvisioned = (n: SIPNumber) => Boolean(n.inboundTrunkId && n.outboundTrunkId && n.dispatchRuleId)

export function SIPNumberDialog({ open, onClose, initial, profiles }: { open: boolean; onClose: () => void; initial?: SIPNumber | null; profiles: AgentProfile[] }) {
  // Remount the form whenever the target changes so state initialises from props.
  return (
    <Dialog open={open} onClose={onClose} title={initial ? 'Дугаар засах' : 'SIP дугаар нэмэх'} description="Дугаар нэмэхэд LiveKit SIP trunk болон dispatch rule автоматаар үүснэ.">
      <SIPNumberForm key={initial?.id ?? 'new'} initial={initial} profiles={profiles} onClose={onClose} />
    </Dialog>
  )
}

function SIPNumberForm({ initial, profiles, onClose }: { initial?: SIPNumber | null; profiles: AgentProfile[]; onClose: () => void }) {
  const save = useSaveSIPNumber()
  const [number, setNumber] = useState(initial?.number ?? '+976')
  const [label, setLabel] = useState(initial?.label ?? '')
  const [agentProfileId, setAgentProfileId] = useState(initial?.agentProfileId ?? '')
  const [allowInbound, setAllowInbound] = useState(initial?.allowInbound ?? true)
  const [allowOutbound, setAllowOutbound] = useState(initial?.allowOutbound ?? true)
  const [endpoint, setEndpoint] = useState(initial?.asteriskEndpoint ?? '')
  const [active, setActive] = useState(initial?.active ?? true)
  const [touched, setTouched] = useState(false)

  const numberError = touched ? validateNumber(number) : null

  function submit(e: FormEvent) {
    e.preventDefault()
    setTouched(true)
    if (validateNumber(number)) return
    const body: SIPNumberBody = {
      number: normalizeNumber(number), label: label.trim(), agentProfileId: agentProfileId || null,
      allowInbound, allowOutbound, asteriskEndpoint: endpoint.trim() || undefined,
      ...(initial ? { active } : {}),
    }
    save.mutate({ id: initial?.id, body }, {
      onSuccess: () => { toast.success(initial ? 'Дугаар шинэчлэгдлээ' : 'Дугаар нэмэгдлээ'); onClose() },
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      <Field label="Дугаар" hint="Олон улсын формат: +976 болон 8 оронтой дугаар (жишээ: +97670001234)" error={numberError}>
        <Input value={number} onChange={(e) => setNumber(e.target.value)} onBlur={() => setTouched(true)} placeholder="+97670001234" inputMode="tel" autoFocus />
      </Field>
      <Field label="Тайлбар">
        <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Жишээ: Борлуулалтын шугам" />
      </Field>
      <Field label="Агент профайл">
        <Select value={agentProfileId} onChange={(e) => setAgentProfileId(e.target.value)} placeholder="— Сонгоогүй —" options={profiles.map((p) => ({ value: p.id, label: p.name }))} />
      </Field>
      <div className="grid gap-2 sm:grid-cols-2">
        <SwitchRow label="Ирж буй дуудлага" checked={allowInbound} onChange={setAllowInbound} />
        <SwitchRow label="Явах дуудлага" checked={allowOutbound} onChange={setAllowOutbound} />
      </div>
      <Field label="Asterisk endpoint" hint="Заавал биш. Жишээ: PJSIP/livekit">
        <Input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} placeholder="PJSIP/livekit" />
      </Field>
      {initial && <SwitchRow label="Идэвхтэй" checked={active} onChange={setActive} />}
      <ErrorNote error={save.error} />
      <div className="flex justify-end gap-2 pt-1">
        <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
        <Button type="submit" loading={save.isPending}>Хадгалах</Button>
      </div>
    </form>
  )
}

export default function SIPNumbersTab() {
  const numbers = useSIPNumbers()
  const profiles = useAgentProfiles()
  const del = useDeleteSIPNumber()
  const provision = useProvisionSIPNumber()
  const [editing, setEditing] = useState<SIPNumber | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<SIPNumber | null>(null)
  const profileName = (id?: string | null) => profiles.data?.find((p) => p.id === id)?.name

  const items = numbers.data ?? []

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <p className="max-w-3xl text-sm text-[var(--fg-muted)]">
          Дугаар нэмэхэд LiveKit SIP trunk болон dispatch rule автоматаар үүснэ. Asterisk талд энэ дугаарыг livekit endpoint рүү чиглүүлнэ (docs/ASTERISK.md).
        </p>
        <Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Дугаар нэмэх</Button>
      </div>

      <Card>
        {numbers.isLoading ? (
          <CardBody className="space-y-2"><Skeleton className="h-9" /><Skeleton className="h-9" /><Skeleton className="h-9" /></CardBody>
        ) : numbers.isError ? (
          <CardBody><ErrorNote error={numbers.error} /></CardBody>
        ) : items.length === 0 ? (
          <EmptyState icon={<Phone className="h-8 w-8" />} title="SIP дугаар бүртгэгдээгүй байна" description="Эхний дугаараа нэмээд агент профайлтай холбоно уу." action={<Button onClick={() => setCreating(true)}><Plus className="h-4 w-4" />Дугаар нэмэх</Button>} />
        ) : (
          <Table>
            <THead><TR><TH>Дугаар</TH><TH>Тайлбар</TH><TH>Агент профайл</TH><TH>Чиглэл</TH><TH>Төлөв</TH><TH>Провижн</TH><TH className="text-right">Үйлдэл</TH></TR></THead>
            <TBody>
              {items.map((n) => (
                <TR key={n.id}>
                  <TD className="font-mono tabular-nums">{fmtPhone(n.number)}</TD>
                  <TD>{n.label || '—'}</TD>
                  <TD>{profileName(n.agentProfileId) ?? <span className="text-[var(--fg-subtle)]">—</span>}</TD>
                  <TD>
                    <div className="flex gap-1.5">
                      <Badge tone={n.allowInbound ? 'success' : 'neutral'}><ArrowDownLeft className="h-3 w-3" />Ирж буй</Badge>
                      <Badge tone={n.allowOutbound ? 'info' : 'neutral'}><ArrowUpRight className="h-3 w-3" />Явах</Badge>
                    </div>
                  </TD>
                  <TD><Badge tone={n.active ? 'success' : 'neutral'} dot>{n.active ? 'Идэвхтэй' : 'Идэвхгүй'}</Badge></TD>
                  <TD>
                    {isProvisioned(n) ? (
                      <div className="flex items-center gap-2" title={`inbound: ${n.inboundTrunkId}\noutbound: ${n.outboundTrunkId}\ndispatch: ${n.dispatchRuleId}`}>
                        <Check className="h-4 w-4 text-emerald-400" aria-label="Провижн хийгдсэн" />
                        <div className="font-mono text-[10px] leading-4 text-[var(--fg-muted)]">
                          <div>in {short(n.inboundTrunkId)}</div><div>out {short(n.outboundTrunkId)}</div><div>rule {short(n.dispatchRuleId)}</div>
                        </div>
                      </div>
                    ) : (
                      <div className="flex items-center gap-2">
                        <Badge tone="warning">Провижн хийгдээгүй</Badge>
                        <Button size="sm" variant="outline" loading={provision.isPending && provision.variables === n.id}
                          onClick={() => provision.mutate(n.id, { onSuccess: () => toast.success('Провижн хийгдлээ'), onError: (e) => toast.error(errMsg(e)) })}>
                          <RefreshCw className="h-3.5 w-3.5" />Провижн
                        </Button>
                      </div>
                    )}
                  </TD>
                  <TD className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button size="icon" variant="ghost" aria-label={`Засах ${n.number}`} onClick={() => setEditing(n)}><Pencil className="h-4 w-4" /></Button>
                      <Button size="icon" variant="ghost" aria-label={`Устгах ${n.number}`} onClick={() => setDeleting(n)}><Trash2 className="h-4 w-4 text-red-400" /></Button>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <SIPNumberDialog open={creating || editing !== null} initial={editing} profiles={profiles.data ?? []} onClose={() => { setCreating(false); setEditing(null) }} />
      <ConfirmDialog
        open={deleting !== null} title="Дугаар устгах уу?" description={deleting ? `${fmtPhone(deleting.number)} — LiveKit trunk болон dispatch rule мөн устна.` : undefined}
        loading={del.isPending} onClose={() => setDeleting(null)}
        onConfirm={() => deleting && del.mutate(deleting.id, { onSuccess: () => { toast.success('Устгагдлаа'); setDeleting(null) }, onError: (e) => toast.error(errMsg(e)) })}
      />
    </div>
  )
}
