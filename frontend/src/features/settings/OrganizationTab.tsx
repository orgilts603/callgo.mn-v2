import { useState, type FormEvent, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { toast } from 'sonner'
import { AlertTriangle, ArrowUpRight, BadgeCheck, Building2, CreditCard, MailWarning, Server, User as UserIcon } from 'lucide-react'
import { roleLabel, useAuth } from '@/app/auth'
import { Badge, Button, Card, CardBody, CardHeader, Field, Input, Select, Skeleton, type BadgeTone } from '@/components/ui'
import type { Organization, Plan, Subscription, SubscriptionStatus } from '@/lib/types'
import { fmtDateTime } from '@/lib/utils'
import { ErrorNote, errMsg } from './common'
import { useOrg, useUpdateOrg } from './hooks'
import { TIMEZONES } from './identity'

const subTone: Record<SubscriptionStatus, BadgeTone> = { trialing: 'info', active: 'success', past_due: 'warning', canceled: 'neutral' }
const subLabel: Record<SubscriptionStatus, string> = { trialing: 'Туршилт', active: 'Идэвхтэй', past_due: 'Төлбөр хоцорсон', canceled: 'Цуцлагдсан' }

function daysLeft(iso?: string | null): number | null {
  if (!iso) return null
  return Math.max(0, Math.ceil((new Date(iso).getTime() - Date.now()) / 86_400_000))
}

function Row({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-[var(--border-subtle)] py-2 text-[13px] last:border-0">
      <span className="text-[var(--fg-muted)]">{k}</span><span className="min-w-0 truncate text-right text-[var(--fg)]">{v}</span>
    </div>
  )
}

/** Red/amber notice when the org is suspended (unpaid invoice) or closed. */
export function OrgStatusBanner({ org }: { org: Pick<Organization, 'status'> }) {
  if (org.status === 'active') return null
  const suspended = org.status === 'suspended'
  return (
    <div role="alert" className="flex items-start gap-3 rounded-[var(--radius)] border border-[var(--danger-border)] bg-[var(--danger-soft)] px-4 py-3 text-[13px] text-[var(--danger-fg)]">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
      <div className="min-w-0 flex-1">
        <div className="font-medium">{suspended ? 'Байгууллагын эрх түр хаагдсан байна' : 'Байгууллага хаагдсан байна'}</div>
        <p className="mt-0.5 text-xs opacity-90">
          {suspended
            ? 'Төлөгдөөгүй нэхэмжлэл байгаа тул дуудлага, кампанит ажил болон өөрчлөлт хийх боломжгүй. Төлбөрөө төлмөгц автоматаар сэргэнэ.'
            : 'Энэ байгууллагын үйл ажиллагаа зогссон. Дэлгэрэнгүйг CallGo-ийн дэмжлэгийн багаас лавлана уу.'}
        </p>
      </div>
      {suspended && (
        <Link to="/settings/billing" className="inline-flex shrink-0 items-center gap-1 text-xs font-medium underline-offset-2 hover:underline">
          Төлбөр төлөх <ArrowUpRight className="h-3.5 w-3.5" />
        </Link>
      )}
    </div>
  )
}

function OrgForm({ org, canEdit }: { org: Organization; canEdit: boolean }) {
  const update = useUpdateOrg()
  const [name, setName] = useState(org.name)
  const [timezone, setTimezone] = useState(org.timezone || 'Asia/Ulaanbaatar')
  const [error, setError] = useState<string | null>(null)
  const zones = TIMEZONES.some((z) => z.value === timezone) ? TIMEZONES : [{ value: timezone, label: timezone }, ...TIMEZONES]
  const dirty = name.trim() !== org.name || timezone !== org.timezone

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) { setError('Байгууллагын нэр хоосон байж болохгүй.'); return }
    setError(null)
    const body: { name?: string; timezone?: string } = {}
    if (name.trim() !== org.name) body.name = name.trim()
    if (timezone !== org.timezone) body.timezone = timezone
    update.mutate(body, {
      onSuccess: () => toast.success('Байгууллагын мэдээлэл хадгалагдлаа'),
      onError: (err) => toast.error(errMsg(err)),
    })
  }

  return (
    <form onSubmit={submit} noValidate className="space-y-4">
      <Field label="Байгууллагын нэр" error={error}>
        <Input value={name} onChange={(e) => setName(e.target.value)} disabled={!canEdit} aria-invalid={!!error || undefined} />
      </Field>
      <Field label="Цагийн бүс" hint="Кампанит ажлын хуваарь, тайлан болон ажлын цагийн тооцоонд ашиглана.">
        <Select value={timezone} onChange={(e) => setTimezone(e.target.value)} options={zones} disabled={!canEdit} />
      </Field>
      <div>
        <Row k="Slug" v={<code className="font-mono text-xs">{org.slug}</code>} />
        <Row k="Үүсгэсэн" v={fmtDateTime(org.createdAt)} />
      </div>
      {canEdit ? (
        <div className="flex justify-end">
          <Button type="submit" loading={update.isPending} disabled={!dirty}>Хадгалах</Button>
        </div>
      ) : (
        <p className="text-xs text-[var(--fg-subtle)]">Зөвхөн эзэмшигч болон админ өөрчлөх эрхтэй.</p>
      )}
    </form>
  )
}

function PlanCard({ org, plan, subscription }: { org: Organization; plan?: Plan | null; subscription?: Subscription | null }) {
  const trialDays = subscription?.status === 'trialing' ? daysLeft(subscription.trialEndsAt ?? subscription.currentPeriodEnd) : null
  return (
    <Card>
      <CardHeader title="Багц" description="Одоогийн захиалга" actions={<CreditCard className="h-4 w-4 text-[var(--fg-muted)]" />} />
      <CardBody className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone="accent" data-testid="plan-badge">{plan?.name || org.planCode || '—'}</Badge>
          {subscription && <Badge tone={subTone[subscription.status]} dot>{subLabel[subscription.status]}</Badge>}
        </div>
        {trialDays !== null && (
          <p className="text-xs text-[var(--fg-muted)]">Туршилтын хугацаа дуусахад <span className="font-medium text-[var(--fg)]">{trialDays}</span> хоног үлдлээ.</p>
        )}
        {subscription && <Row k="Одоогийн үе" v={`${fmtDateTime(subscription.currentPeriodStart).slice(0, 10)} — ${fmtDateTime(subscription.currentPeriodEnd).slice(0, 10)}`} />}
        {plan && <Row k="Багтсан минут" v={plan.includedMinutes.toLocaleString('en-US')} />}
        <Link to="/settings/billing" className="inline-flex items-center gap-1 text-xs font-medium text-[var(--fg-muted)] underline-offset-2 hover:text-[var(--fg)] hover:underline">
          Багц, төлбөрийн тохиргоо <ArrowUpRight className="h-3.5 w-3.5" />
        </Link>
      </CardBody>
    </Card>
  )
}

const ENDPOINTS: { method: string; path: string; note: string }[] = [
  { method: 'GET', path: '/internal/agent/bootstrap', note: 'Агент дуудлага эхлэхэд профайл, LLM, толь бичгийг авна' },
  { method: 'POST', path: '/internal/agent/events', note: 'Транскрипт, төлөв, дуудлагын дүнг backend рүү илгээнэ' },
  { method: 'POST', path: '/api/livekit/webhook', note: 'LiveKit-ээс room / participant / egress event хүлээн авна' },
]

export default function OrganizationTab() {
  const q = useOrg()
  const user = useAuth((s) => s.user)
  const canEdit = user?.role === 'owner' || user?.role === 'admin'
  const data = q.data

  return (
    <div className="space-y-4">
      {data?.org && <OrgStatusBanner org={data.org} />}
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader title="Байгууллага" description="Нэр, цагийн бүс" actions={<Building2 className="h-4 w-4 text-[var(--fg-muted)]" />} />
          <CardBody>
            {q.isLoading ? <div className="space-y-2"><Skeleton className="h-8" /><Skeleton className="h-8" /><Skeleton className="h-6" /></div>
              : q.isError ? <ErrorNote error={q.error} />
              : data?.org && <OrgForm key={data.org.updatedAt} org={data.org} canEdit={canEdit} />}
          </CardBody>
        </Card>

        <div className="space-y-4">
          {data?.org && <PlanCard org={data.org} plan={data.plan} subscription={data.subscription} />}
          {user && (
            <Card>
              <CardHeader title="Миний бүртгэл" actions={<UserIcon className="h-4 w-4 text-[var(--fg-muted)]" />} />
              <CardBody className="py-2">
                <Row k="Нэр" v={user.name} />
                <Row k="И-мэйл" v={
                  <span className="inline-flex items-center gap-1.5">
                    {user.email}
                    {user.emailVerifiedAt
                      ? <BadgeCheck className="h-3.5 w-3.5 text-[var(--success)]" aria-label="Баталгаажсан" />
                      : <MailWarning className="h-3.5 w-3.5 text-[var(--warning)]" aria-label="Баталгаажаагүй" />}
                  </span>
                } />
                <Row k="Эрх" v={<Badge tone="neutral">{roleLabel(user.role)}</Badge>} />
                <Row k="Сүүлд нэвтэрсэн" v={fmtDateTime(user.lastLoginAt)} />
              </CardBody>
            </Card>
          )}
        </div>
      </div>

      <Card>
        <CardHeader title="Холболтын мэдээлэл" description="Зөвхөн унших. Тохиргоо нь орчны хувьсагчаар хийгдэнэ." actions={<Server className="h-4 w-4 text-[var(--fg-muted)]" />} />
        <CardBody className="grid gap-4 text-xs md:grid-cols-2">
          <ul className="space-y-2">
            {ENDPOINTS.map((e) => (
              <li key={e.path}>
                <div className="flex items-center gap-2"><Badge tone="neutral" className="font-mono">{e.method}</Badge><code className="font-mono text-[var(--fg)]">{e.path}</code></div>
                <div className="mt-0.5 pl-1 text-[var(--fg-muted)]">{e.note}</div>
              </li>
            ))}
          </ul>
          <div className="rounded-[var(--radius-sm)] border border-[var(--border)] bg-[var(--surface-inset)] p-3 text-[var(--fg-muted)]">
            <div className="mb-1 font-medium text-[var(--fg)]">Agent token</div>
            <code className="font-mono text-[var(--fg)]">/internal/agent/*</code> endpoint бүр <code className="font-mono text-[var(--fg)]">X-Agent-Token</code> header шаарддаг
            (<code className="font-mono text-[var(--fg)]">CALLGO_AGENT_TOKEN</code>). Гадаад системээс API ашиглах бол «API түлхүүр» табаас түлхүүр үүсгэнэ үү.
          </div>
        </CardBody>
      </Card>
    </div>
  )
}
