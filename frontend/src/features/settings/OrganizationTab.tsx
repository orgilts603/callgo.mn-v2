import { Building2, Server, User as UserIcon } from 'lucide-react'
import { Badge, Card, CardBody, CardHeader, Skeleton } from '@/components/ui'
import { fmtDateTime } from '@/lib/utils'
import { useMe } from './hooks'
import { ErrorNote } from './common'

const roleLabel = { owner: 'Эзэмшигч', admin: 'Админ', operator: 'Оператор' } as const

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-[var(--border)] py-2 text-sm last:border-0">
      <span className="text-[var(--fg-muted)]">{k}</span><span className="text-right text-[var(--fg)]">{v}</span>
    </div>
  )
}

const ENDPOINTS: { method: string; path: string; note: string }[] = [
  { method: 'GET', path: '/internal/agent/bootstrap', note: 'Агент дуудлага эхлэхэд профайл, LLM, толь бичгийг авна' },
  { method: 'POST', path: '/internal/agent/events', note: 'Транскрипт, төлөв, дуудлагын дүнг backend рүү илгээнэ' },
  { method: 'POST', path: '/internal/agent/lexicon-hit', note: 'Толь бичгийн засварын хэрэглээг тоолно' },
  { method: 'POST', path: '/api/livekit/webhook', note: 'LiveKit-ээс room / participant / egress event хүлээн авна' },
  { method: 'POST', path: ':8090/test-llm', note: 'Agent worker: LLM тохиргоог турших (backend proxy хийнэ)' },
]

export default function OrganizationTab() {
  const me = useMe()
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader title="Байгууллага" description="Одоогийн нэвтэрсэн байгууллагын мэдээлэл" />
        <CardBody>
          {me.isLoading ? <div className="space-y-2"><Skeleton className="h-6" /><Skeleton className="h-6" /><Skeleton className="h-6" /></div>
            : me.isError ? <ErrorNote error={me.error} />
            : me.data && (
              <>
                <div className="mb-3 flex items-center gap-2 text-[var(--fg-muted)]"><Building2 className="h-4 w-4" /><span className="text-xs uppercase tracking-wide">Байгууллага</span></div>
                <Row k="Нэр" v={me.data.org.name} />
                <Row k="Slug" v={<code className="font-mono text-xs">{me.data.org.slug}</code>} />
                <Row k="Үүсгэсэн" v={fmtDateTime(me.data.org.createdAt)} />
                <div className="mb-3 mt-5 flex items-center gap-2 text-[var(--fg-muted)]"><UserIcon className="h-4 w-4" /><span className="text-xs uppercase tracking-wide">Хэрэглэгч</span></div>
                <Row k="Нэр" v={me.data.user.name} />
                <Row k="И-мэйл" v={me.data.user.email} />
                <Row k="Эрх" v={<Badge tone="accent">{roleLabel[me.data.user.role] ?? me.data.user.role}</Badge>} />
              </>
            )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader title="Холболтын мэдээлэл" description="Зөвхөн унших. Тохиргоо нь орчны хувьсагчаар хийгдэнэ." actions={<Server className="h-4 w-4 text-[var(--fg-muted)]" />} />
        <CardBody className="space-y-4">
          <div>
            <div className="mb-2 text-xs font-medium uppercase tracking-wide text-[var(--fg-muted)]">Дотоод endpoint-ууд</div>
            <ul className="space-y-2">
              {ENDPOINTS.map((e) => (
                <li key={e.path} className="text-xs">
                  <div className="flex items-center gap-2"><Badge tone={e.method === 'GET' ? 'info' : 'success'} className="font-mono">{e.method}</Badge><code className="font-mono text-[var(--fg)]">{e.path}</code></div>
                  <div className="mt-0.5 pl-1 text-[var(--fg-muted)]">{e.note}</div>
                </li>
              ))}
            </ul>
          </div>
          <div className="rounded-md border border-[var(--border)] bg-[var(--surface-0)] p-3 text-xs text-[var(--fg-muted)]">
            <div className="mb-1 font-medium text-[var(--fg)]">Agent token</div>
            <code className="font-mono text-[var(--fg)]">/internal/agent/*</code> endpoint бүр <code className="font-mono text-[var(--fg)]">X-Agent-Token</code> header шаарддаг. Утга нь backend болон agent worker дээрх{' '}
            <code className="font-mono text-[var(--fg)]">CALLGO_AGENT_TOKEN</code> орчны хувьсагчтай ижил байх ёстой. Token-ыг энд харуулахгүй.
          </div>
          <div className="text-xs text-[var(--fg-muted)]">Asterisk-ээс LiveKit рүү чиглүүлэх заавар: <code className="font-mono text-[var(--fg)]">docs/ASTERISK.md</code></div>
        </CardBody>
      </Card>
    </div>
  )
}
