import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Sparkles } from 'lucide-react'
import { Button, Card, EmptyState } from '@/components/ui'
import { isFeatureUnavailable } from './hooks'

/** Shown when the plan does not include a feature (API answers 403 `feature_unavailable`). */
export function UpgradeCard({ feature }: { feature: string }) {
  return (
    <Card data-testid="upgrade-card">
      <EmptyState
        icon={<Sparkles className="h-8 w-8" />}
        title={`${feature} таны багцад ороогүй байна`}
        description="Энэ боломжийг ашиглахын тулд багцаа шинэчилнэ үү."
        action={<Link to="/settings/billing"><Button>Багц шинэчлэх</Button></Link>}
      />
    </Card>
  )
}

/** Renders the upgrade card instead of `children` when `error` is a feature-gate failure. */
export function FeatureGate({ error, feature, children }: { error: unknown; feature: string; children: ReactNode }) {
  if (isFeatureUnavailable(error)) return <UpgradeCard feature={feature} />
  return <>{children}</>
}
