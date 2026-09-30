import { Activity, CheckCircle2, Frown, PhoneCall, Smile, Timer } from 'lucide-react'
import { Skeleton, StatCard } from '@/components/ui'
import { fmtDuration } from '@/lib/utils'
import { useLive } from '@/lib/ws'
import type { CallStats } from '@/lib/types'
import { fmtNumber, fmtPercent } from './format'

function StatSkeleton() {
  return (
    <div className="rounded-[var(--radius-lg)] border border-[var(--border)] border-t-[var(--border-highlight)] bg-[var(--surface-1)] px-4 py-3.5" data-testid="stat-skeleton">
      <Skeleton className="h-3 w-20" />
      <Skeleton className="mt-3 h-6 w-16" />
      <Skeleton className="mt-2 h-3 w-24" />
    </div>
  )
}

export function StatsGrid({ stats, loading }: { stats?: CallStats; loading: boolean }) {
  const wsOpen = useLive((s) => s.status === 'open')
  const liveActive = useLive((s) => Object.keys(s.activeCalls).length)

  if (loading || !stats) {
    return (
      <div className="grid grid-cols-3 gap-3 xl:grid-cols-6">
        {Array.from({ length: 6 }, (_, i) => <StatSkeleton key={i} />)}
      </div>
    )
  }
  // The socket snapshot is fresher than the last /stats fetch while it is connected.
  const active = wsOpen ? liveActive : stats.activeCalls
  return (
    <div className="grid grid-cols-3 gap-3 xl:grid-cols-6">
      <StatCard label="Нийт дуудлага" icon={<PhoneCall />} value={fmtNumber(stats.totalCalls)}
        hint={`Өнөөдөр ${stats.inboundToday} ирсэн · ${stats.outboundToday} гарсан`} />
      <StatCard label="Идэвхтэй" icon={<Activity />}
        value={<span className="inline-flex items-center gap-2">{active}{active > 0 && <span className="h-2 w-2 animate-pulse rounded-full bg-[var(--success)]" aria-hidden />}</span>}
        hint={wsOpen ? 'Шууд шинэчлэгдэж байна' : 'Сүүлийн шинэчлэлтээр'} />
      <StatCard label="Өнөөдөр дууссан" icon={<CheckCircle2 />} value={fmtNumber(stats.completedToday)} hint="Амжилттай холбогдсон" />
      <StatCard label="Дундаж хугацаа" icon={<Timer />} value={fmtDuration(stats.avgDurationSec)} hint="Нэг дуудлагад" />
      <StatCard label="Эерэг %" icon={<Smile />} value={fmtPercent(stats.positiveRatio)} hint="Сэтгэл ханамжтай яриа" />
      <StatCard label="Сөрөг %" icon={<Frown />} value={fmtPercent(stats.negativeRatio)} hint="Анхаарал шаардлагатай" />
    </div>
  )
}
