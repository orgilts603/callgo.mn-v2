import { Navigate, NavLink, useParams } from 'react-router-dom'
import { Bot, Building2, Cpu, Phone } from 'lucide-react'
import type { ComponentType } from 'react'
import { PageHeader } from '@/components/ui'
import { cn } from '@/lib/utils'
import SIPNumbersTab from './SIPNumbersTab'
import AgentProfilesTab from './AgentProfilesTab'
import LLMConfigsTab from './LLMConfigsTab'
import OrganizationTab from './OrganizationTab'

export const SETTINGS_TABS = [
  { id: 'sip-numbers', label: 'SIP дугаарууд', icon: Phone, Component: SIPNumbersTab },
  { id: 'agent-profiles', label: 'Агент профайл', icon: Bot, Component: AgentProfilesTab },
  { id: 'llm', label: 'LLM тохиргоо', icon: Cpu, Component: LLMConfigsTab },
  { id: 'organization', label: 'Байгууллага', icon: Building2, Component: OrganizationTab },
] as const satisfies readonly { id: string; label: string; icon: ComponentType<{ className?: string }>; Component: ComponentType }[]

export default function SettingsPage() {
  const { tab } = useParams<{ tab?: string }>()
  if (!tab) return <Navigate to="/settings/sip-numbers" replace />
  const active = SETTINGS_TABS.find((t) => t.id === tab)
  if (!active) return <Navigate to="/settings/sip-numbers" replace />
  const Active = active.Component
  return (
    <div>
      <PageHeader title="Тохиргоо" description="SIP дугаар, агент профайл, LLM болон байгууллагын тохиргоо" />
      <nav className="mb-6 flex gap-1 overflow-x-auto border-b border-[var(--border)]" aria-label="Тохиргооны табууд">
        {SETTINGS_TABS.map((t) => (
          <NavLink
            key={t.id} to={`/settings/${t.id}`}
            className={({ isActive }) => cn(
              'inline-flex items-center gap-2 whitespace-nowrap border-b-2 px-4 py-2.5 text-sm font-medium transition-colors -mb-px',
              isActive ? 'border-[var(--accent)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]',
            )}
          >
            <t.icon className="h-4 w-4" />{t.label}
          </NavLink>
        ))}
      </nav>
      <Active />
    </div>
  )
}
