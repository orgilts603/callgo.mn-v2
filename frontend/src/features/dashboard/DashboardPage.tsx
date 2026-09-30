import { useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { AlertTriangle, Radio, RefreshCw } from 'lucide-react'
import { Button, PageHeader } from '@/components/ui'
import { useDailyStats, useLiveDashboardRefresh, useRecentCalls, useStats } from './api'
import { DailyChart } from './DailyChart'
import { RecentCalls } from './RecentCalls'
import { StatsGrid } from './StatsGrid'
import { SystemCard } from './SystemCard'

const DAYS = 14

export default function DashboardPage() {
  const qc = useQueryClient()
  const stats = useStats()
  const daily = useDailyStats(DAYS)
  const recent = useRecentCalls(10)
  useLiveDashboardRefresh()

  const refreshing = stats.isFetching || daily.isFetching || recent.isFetching
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ['stats'] })
    void qc.invalidateQueries({ queryKey: ['calls', 'recent'] })
  }

  return (
    <div className="space-y-4">
      <PageHeader
        title="Хяналтын самбар"
        description="Дуудлагын идэвх, чанарын үзүүлэлтүүд бодит цагаар"
        actions={
          <>
            <Button variant="ghost" size="sm" onClick={refresh} aria-label="Шинэчлэх" disabled={refreshing}>
              <RefreshCw className={refreshing ? 'animate-spin' : undefined} /> Шинэчлэх
            </Button>
            <Link to="/live" className="inline-flex h-7 items-center gap-1.5 rounded-[var(--radius-sm)] bg-[var(--accent)] px-2.5 text-xs font-medium text-white shadow-[var(--shadow-sm)] transition-colors hover:bg-[var(--accent-hover)]">
              <Radio className="h-3.5 w-3.5" /> Live Desk
            </Link>
          </>
        }
      />

      {stats.isError && (
        <div role="alert" className="flex items-center gap-2 rounded-[var(--radius)] border border-[var(--warning-border)] bg-[var(--warning-soft)] px-3 py-2 text-xs text-[var(--warning-fg)]">
          <AlertTriangle className="h-3.5 w-3.5" />
          Статистик ачаалж чадсангүй.
          <button type="button" onClick={() => void stats.refetch()} className="ml-auto font-medium underline-offset-2 hover:underline">Дахин оролдох</button>
        </div>
      )}

      <StatsGrid stats={stats.data} loading={stats.isPending && !stats.isError} />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <div className="xl:col-span-2"><DailyChart data={daily.data} loading={daily.isPending} days={DAYS} /></div>
        <SystemCard />
      </div>

      <RecentCalls calls={recent.data?.items} loading={recent.isPending} error={recent.isError} onRetry={() => void recent.refetch()} />
    </div>
  )
}
