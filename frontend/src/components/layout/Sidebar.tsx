import { NavLink } from 'react-router-dom'
import { PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { useAuth } from '@/app/auth'
import { useUI } from '@/app/ui'
import { useLive } from '@/lib/ws'
import { cn } from '@/lib/utils'
import { LogoMark } from './Logo'
import { NAV_SECTIONS, visibleNavItems, type NavItem } from './nav'

function NavEntry({ item, collapsed, count }: { item: NavItem; collapsed: boolean; count?: number }) {
  const Icon = item.icon
  return (
    <NavLink
      to={item.to}
      end={item.to === '/'}
      title={collapsed ? `${item.label} (g ${item.hotkey})` : undefined}
      aria-label={collapsed ? item.label : undefined}
      className={({ isActive }) =>
        cn(
          'group relative flex h-8 items-center gap-2.5 rounded-[var(--radius-sm)] px-2 text-[13px] font-medium transition-colors',
          collapsed && 'justify-center px-0',
          isActive
            ? 'bg-[var(--surface-3)] text-[var(--fg)] shadow-[inset_0_0_0_1px_var(--border)]'
            : 'text-[var(--fg-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)]',
        )
      }
    >
      {({ isActive }) => (
        <>
          {isActive && <span className="absolute -left-2 top-1.5 bottom-1.5 w-[2px] rounded-full bg-[var(--accent)]" aria-hidden />}
          <Icon className={cn('h-4 w-4 shrink-0', isActive ? 'text-[var(--accent-fg)]' : 'text-[var(--fg-subtle)] group-hover:text-[var(--fg-muted)]')} strokeWidth={1.9} />
          {!collapsed && <span className="truncate">{item.label}</span>}
          {!!count && (
            collapsed
              ? <span className="absolute right-1.5 top-1.5 h-1.5 w-1.5 rounded-full bg-[var(--success)]" aria-hidden />
              : <span className="tabular ml-auto rounded-full bg-[var(--success-soft)] px-1.5 text-[10.5px] leading-4 font-semibold text-[var(--success-fg)]">{count}</span>
          )}
        </>
      )}
    </NavLink>
  )
}

export function Sidebar() {
  const collapsed = useUI((s) => s.sidebarCollapsed)
  const toggle = useUI((s) => s.toggleSidebar)
  const org = useAuth((s) => s.org)
  const isPlatformAdmin = useAuth((s) => !!s.user?.isPlatformAdmin)
  const items = visibleNavItems(isPlatformAdmin)
  const activeCount = useLive((s) => Object.keys(s.activeCalls).length)

  return (
    <aside
      aria-label="Үндсэн цэс"
      data-collapsed={collapsed || undefined}
      className={cn(
        'sticky top-0 flex h-screen shrink-0 flex-col border-r border-[var(--border-subtle)] bg-[var(--surface-1)] transition-[width] duration-150 ease-out',
        collapsed ? 'w-[var(--sidebar-w-collapsed)]' : 'w-[var(--sidebar-w)]',
      )}
    >
      <div className={cn('flex h-[var(--topbar-h)] shrink-0 items-center gap-2.5 border-b border-[var(--border-subtle)]', collapsed ? 'justify-center px-0' : 'px-4')}>
        <LogoMark />
        {!collapsed && (
          <div className="min-w-0 leading-tight">
            <div className="text-[13.5px] font-semibold tracking-[-0.015em] text-[var(--fg)]">CallGo<span className="text-[var(--fg-subtle)]">.mn</span></div>
            <div className="truncate text-[11px] text-[var(--fg-subtle)]">{org?.name ?? 'Voice AI платформ'}</div>
          </div>
        )}
      </div>

      <nav className={cn('flex-1 overflow-y-auto py-3', collapsed ? 'px-2' : 'px-3')}>
        {NAV_SECTIONS.map((sec, i) => (
          <div key={sec.id} className={cn(i > 0 && 'mt-4')}>
            {collapsed
              ? i > 0 && <div className="mx-2 mb-3 border-t border-[var(--border-subtle)]" />
              : <div className="mb-1 px-2 text-[10.5px] font-medium uppercase tracking-[0.06em] text-[var(--fg-subtle)]">{sec.label}</div>}
            <ul className="space-y-0.5">
              {items.filter((n) => n.section === sec.id).map((item) => (
                <li key={item.to}><NavEntry item={item} collapsed={collapsed} count={item.to === '/live' ? activeCount : undefined} /></li>
              ))}
            </ul>
          </div>
        ))}
      </nav>

      <div className={cn('shrink-0 border-t border-[var(--border-subtle)] p-2', collapsed && 'flex justify-center')}>
        <button
          type="button"
          onClick={toggle}
          aria-label={collapsed ? 'Цэсийг дэлгэх' : 'Цэсийг хураах'}
          aria-expanded={!collapsed}
          title={collapsed ? 'Цэсийг дэлгэх ([)' : undefined}
          className={cn('flex h-8 items-center gap-2 rounded-[var(--radius-sm)] text-xs text-[var(--fg-subtle)] transition-colors hover:bg-[var(--surface-2)] hover:text-[var(--fg)]', collapsed ? 'w-8 justify-center' : 'w-full px-2')}
        >
          {collapsed ? <PanelLeftOpen className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
          {!collapsed && <><span>Цэсийг хураах</span><kbd className="ml-auto">[</kbd></>}
        </button>
      </div>
    </aside>
  )
}
