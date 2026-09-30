import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { Ban, CheckCircle2, Clock, Mail, RotateCw, Trash2, UserPlus, Users, X } from 'lucide-react'
import { roleLabel, useAuth } from '@/app/auth'
import {
  Badge, Button, Card, CardHeader, ConfirmDialog, Dialog, EmptyState, Field, Input, Select, Skeleton, Table, TBody, TD, TH, THead, TR,
  type BadgeTone,
} from '@/components/ui'
import { HttpError } from '@/lib/api'
import type { Invitation, Role, User, UserStatus } from '@/lib/types'
import { fmtDateTime } from '@/lib/utils'
import { ErrorNote, errMsg } from './common'
import { useCancelInvitation, useInviteMember, useMembers, useRemoveMember, useResendInvitation, useUpdateMember } from './hooks'

const statusTone: Record<UserStatus, BadgeTone> = { active: 'success', invited: 'warning', disabled: 'danger' }
const statusLabel: Record<UserStatus, string> = { active: 'Идэвхтэй', invited: 'Урилгатай', disabled: 'Идэвхгүй' }

export function roleOptions(myRole: Role | undefined): { value: Role; label: string }[] {
  const roles: Role[] = myRole === 'owner' ? ['owner', 'admin', 'operator'] : ['admin', 'operator']
  return roles.map((r) => ({ value: r, label: roleLabel(r) }))
}

export interface MemberGuard { canManage: boolean; reason?: string }

/**
 * Mirrors the backend rules so the UI never offers an action that will be refused:
 * nobody edits themselves, the last active owner cannot be demoted/disabled/removed,
 * only owners touch other owners, operators manage nobody.
 */
export function memberGuard(member: User, me: User | null, activeOwners: number): MemberGuard {
  if (!me || (me.role !== 'owner' && me.role !== 'admin')) return { canManage: false, reason: 'Эрх хүрэхгүй' }
  if (member.id === me.id) return { canManage: false, reason: 'Өөрийн эрхийг өөрчлөх боломжгүй' }
  if (member.role === 'owner' && member.status === 'active' && activeOwners <= 1) return { canManage: false, reason: 'Сүүлийн эзэмшигч' }
  if (member.role === 'owner' && me.role !== 'owner') return { canManage: false, reason: 'Эзэмшигчийг зөвхөн эзэмшигч өөрчилнө' }
  return { canManage: true }
}

function inviteError(err: unknown): string {
  if (err instanceof HttpError) {
    if (err.status === 409) return 'Энэ и-мэйлтэй гишүүн аль хэдийн байна.'
    if (err.status === 429 && err.code === 'quota_exceeded') return 'Таны багцын хэрэглэгчийн дээд хязгаарт хүрсэн байна.'
  }
  return errMsg(err)
}

function InviteDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  if (!open) return null
  return <InviteForm onClose={onClose} />
}

function InviteForm({ onClose }: { onClose: () => void }) {
  const me = useAuth((s) => s.user)
  const invite = useInviteMember()
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('operator')
  const [emailError, setEmailError] = useState<string | null>(null)
  const quota = invite.error instanceof HttpError && invite.error.code === 'quota_exceeded'

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())) { setEmailError('Зөв и-мэйл хаяг оруулна уу.'); return }
    setEmailError(null)
    invite.mutate({ email: email.trim().toLowerCase(), role }, {
      onSuccess: () => { toast.success(`${email.trim()} хаяг руу урилга илгээлээ`); onClose() },
    })
  }

  return (
    <Dialog open onClose={onClose} title="Гишүүн урих" description="Урилгын холбоос бүхий и-мэйл илгээгдэнэ. Холбоос 7 хоног хүчинтэй.">
      <form onSubmit={submit} noValidate className="space-y-4">
        <Field label="И-мэйл" error={emailError}>
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@company.mn" autoFocus aria-invalid={!!emailError || undefined} />
        </Field>
        <Field label="Эрх" hint="Оператор: дуудлага, кампанит ажил. Админ: бүх тохиргоо. Эзэмшигч: төлбөр, гишүүд.">
          <Select value={role} onChange={(e) => setRole(e.target.value as Role)} options={roleOptions(me?.role)} />
        </Field>
        {invite.error ? (
          <div role="alert" className="rounded-[var(--radius-sm)] border border-[var(--danger-border)] bg-[var(--danger-soft)] px-3 py-2 text-xs text-[var(--danger-fg)]">
            {inviteError(invite.error)}
            {quota && <> <Link to="/settings/billing" className="font-medium underline">Багц ахиулах</Link></>}
          </div>
        ) : null}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>Болих</Button>
          <Button type="submit" loading={invite.isPending}><Mail /> Урилга илгээх</Button>
        </div>
      </form>
    </Dialog>
  )
}

function MemberRow({ member, me, activeOwners, onRemove }: { member: User; me: User | null; activeOwners: number; onRemove: (u: User) => void }) {
  const update = useUpdateMember()
  const guard = memberGuard(member, me, activeOwners)
  const isSelf = member.id === me?.id
  const options = roleOptions(me?.role)
  const roleOpts = options.some((o) => o.value === member.role) ? options : [{ value: member.role, label: roleLabel(member.role) }, ...options]
  const disabled = member.status === 'disabled'

  const change = (body: { role?: Role; status?: 'active' | 'disabled' }, ok: string) =>
    update.mutate({ id: member.id, body }, { onSuccess: () => toast.success(ok), onError: (err) => toast.error(errMsg(err)) })

  return (
    <TR data-testid={`member-${member.id}`}>
      <TD>
        <div className="font-medium">{member.name || '—'}{isSelf && <span className="ml-1.5 text-xs font-normal text-[var(--fg-subtle)]">(та)</span>}</div>
      </TD>
      <TD className="text-[var(--fg-muted)]">{member.email}</TD>
      <TD>
        <Select
          aria-label={`${member.email} эрх`} className="h-7 w-36 text-xs"
          value={member.role} options={roleOpts} disabled={!guard.canManage || update.isPending}
          title={guard.reason}
          onChange={(e) => change({ role: e.target.value as Role }, 'Эрх шинэчлэгдлээ')}
        />
      </TD>
      <TD><Badge tone={statusTone[member.status]} dot>{statusLabel[member.status]}</Badge></TD>
      <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtDateTime(member.lastLoginAt)}</TD>
      <TD>
        <div className="flex justify-end gap-1">
          {member.status !== 'invited' && (
            <Button
              size="icon" variant="ghost" disabled={!guard.canManage || update.isPending}
              title={guard.reason ?? (disabled ? 'Идэвхжүүлэх' : 'Идэвхгүй болгох')}
              aria-label={`${member.email} ${disabled ? 'идэвхжүүлэх' : 'идэвхгүй болгох'}`}
              onClick={() => change({ status: disabled ? 'active' : 'disabled' }, disabled ? 'Гишүүн идэвхжлээ' : 'Гишүүн идэвхгүй боллоо')}
            >
              {disabled ? <CheckCircle2 /> : <Ban />}
            </Button>
          )}
          <Button size="icon" variant="ghost" disabled={!guard.canManage} title={guard.reason ?? 'Хасах'}
            aria-label={`${member.email} хасах`} onClick={() => onRemove(member)}
            className="hover:text-[var(--danger-fg)]">
            <Trash2 />
          </Button>
        </div>
      </TD>
    </TR>
  )
}

function InvitationRow({ inv, canManage }: { inv: Invitation; canManage: boolean }) {
  const resend = useResendInvitation()
  const cancel = useCancelInvitation()
  const expired = new Date(inv.expiresAt).getTime() < Date.now()
  return (
    <TR data-testid={`invitation-${inv.id}`}>
      <TD className="font-medium">{inv.email}</TD>
      <TD><Badge tone="neutral">{roleLabel(inv.role)}</Badge></TD>
      <TD className="whitespace-nowrap text-xs text-[var(--fg-muted)]">{fmtDateTime(inv.createdAt)}</TD>
      <TD className="whitespace-nowrap text-xs">
        {expired ? <Badge tone="warning">Хугацаа дууссан</Badge> : <span className="text-[var(--fg-muted)]">{fmtDateTime(inv.expiresAt)}</span>}
      </TD>
      <TD>
        {canManage && (
          <div className="flex justify-end gap-1">
            <Button size="sm" variant="ghost" loading={resend.isPending} aria-label={`${inv.email} урилгыг дахин илгээх`}
              onClick={() => resend.mutate(inv, { onSuccess: () => toast.success('Урилгыг дахин илгээлээ'), onError: (err) => toast.error(inviteError(err)) })}>
              <RotateCw /> Дахин илгээх
            </Button>
            <Button size="sm" variant="ghost" loading={cancel.isPending} aria-label={`${inv.email} урилгыг цуцлах`} className="hover:text-[var(--danger-fg)]"
              onClick={() => cancel.mutate(inv.id, { onSuccess: () => toast.success('Урилга цуцлагдлаа'), onError: (err) => toast.error(errMsg(err)) })}>
              <X /> Цуцлах
            </Button>
          </div>
        )}
      </TD>
    </TR>
  )
}

export default function MembersTab() {
  const me = useAuth((s) => s.user)
  const q = useMembers()
  const remove = useRemoveMember()
  const [inviting, setInviting] = useState(false)
  const [removing, setRemoving] = useState<User | null>(null)
  const canManage = me?.role === 'owner' || me?.role === 'admin'

  if (q.error instanceof HttpError && q.error.status === 403) {
    return (
      <Card>
        <EmptyState icon={<Users />} title="Хандах эрхгүй" description="Гишүүдийн жагсаалтыг зөвхөн эзэмшигч болон админ харах боломжтой." />
      </Card>
    )
  }

  const members = q.data?.items ?? []
  const pending = (q.data?.invitations ?? []).filter((i) => !i.acceptedAt)
  const activeOwners = members.filter((u) => u.role === 'owner' && u.status === 'active').length

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader
          title="Гишүүд" description={q.data ? `${members.length} гишүүн` : 'Байгууллагын хэрэглэгчид ба тэдгээрийн эрх'}
          actions={canManage && <Button size="sm" onClick={() => setInviting(true)}><UserPlus /> Гишүүн урих</Button>}
        />
        {q.isLoading ? (
          <div className="space-y-2 p-4"><Skeleton className="h-8" /><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
        ) : q.isError ? (
          <div className="p-4"><ErrorNote error={q.error} /></div>
        ) : members.length === 0 ? (
          <EmptyState icon={<Users />} title="Гишүүн алга" />
        ) : (
          <Table>
            <THead><TR><TH>Нэр</TH><TH>И-мэйл</TH><TH>Эрх</TH><TH>Төлөв</TH><TH>Сүүлд нэвтэрсэн</TH><TH className="text-right"><span className="sr-only">Үйлдэл</span></TH></TR></THead>
            <TBody>
              {members.map((m) => <MemberRow key={m.id} member={m} me={me} activeOwners={activeOwners} onRemove={setRemoving} />)}
            </TBody>
          </Table>
        )}
      </Card>

      {pending.length > 0 && (
        <Card>
          <CardHeader title="Хүлээгдэж буй урилга" description={`${pending.length} урилга хариу хүлээж байна`} actions={<Clock className="h-4 w-4 text-[var(--fg-muted)]" />} />
          <Table>
            <THead><TR><TH>И-мэйл</TH><TH>Эрх</TH><TH>Илгээсэн</TH><TH>Дуусах</TH><TH className="text-right"><span className="sr-only">Үйлдэл</span></TH></TR></THead>
            <TBody>{pending.map((inv) => <InvitationRow key={inv.id} inv={inv} canManage={canManage} />)}</TBody>
          </Table>
        </Card>
      )}

      <InviteDialog open={inviting} onClose={() => setInviting(false)} />
      <ConfirmDialog
        open={!!removing} onClose={() => setRemoving(null)}
        title="Гишүүнийг хасах"
        description={removing ? `${removing.name || removing.email} (${removing.email}) байгууллагаас хасагдаж, бүх нэвтрэлт нь цуцлагдана.` : undefined}
        confirmLabel="Хасах"
        onConfirm={async () => {
          if (!removing) return
          try {
            await remove.mutateAsync(removing.id)
            toast.success('Гишүүнийг хаслаа')
          } catch (err) {
            toast.error(errMsg(err))
            throw err
          }
        }}
      />
    </div>
  )
}
