import type { Ref } from 'react'
import { Breadcrumbs } from './Breadcrumbs'
import { ActiveCallsBadge, LiveStatusPill } from './LiveStatus'
import { SearchBox } from './SearchBox'
import { UserMenu } from './UserMenu'

export function Topbar({ searchRef }: { searchRef?: Ref<HTMLInputElement> }) {
  return (
    <header className="sticky top-0 z-30 flex h-[var(--topbar-h)] shrink-0 items-center gap-4 border-b border-[var(--border-subtle)] bg-[var(--surface-0)]/85 px-4 backdrop-blur-md xl:px-6">
      <Breadcrumbs className="flex-1" />
      <SearchBox ref={searchRef} className="w-56 xl:w-72" />
      <div className="flex items-center gap-2">
        <LiveStatusPill />
        <ActiveCallsBadge />
      </div>
      <div className="h-5 w-px bg-[var(--border-subtle)]" aria-hidden />
      <UserMenu />
    </header>
  )
}
