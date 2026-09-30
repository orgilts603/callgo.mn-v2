import type { LucideIcon } from 'lucide-react'
import { BarChart3, BookA, Contact, History, LayoutDashboard, Megaphone, PhoneForwarded, Radio, Settings, ShieldCheck } from 'lucide-react'

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  /** Second key of the "g <key>" navigation chord. */
  hotkey: string
  section: 'main' | 'manage' | 'system'
  /** Shown only to platform admins (user.isPlatformAdmin). */
  platformAdmin?: boolean
}

export const NAV_ITEMS: NavItem[] = [
  { to: '/', label: 'Хяналтын самбар', icon: LayoutDashboard, hotkey: 'd', section: 'main' },
  { to: '/live', label: 'Live Desk', icon: Radio, hotkey: 'l', section: 'main' },
  { to: '/calls', label: 'Дуудлагын түүх', icon: History, hotkey: 'c', section: 'main' },
  { to: '/analytics', label: 'Аналитик', icon: BarChart3, hotkey: 'a', section: 'main' },
  { to: '/contacts', label: 'Харилцагчид', icon: Contact, hotkey: 'k', section: 'manage' },
  { to: '/campaigns', label: 'Кампанит ажил', icon: Megaphone, hotkey: 'p', section: 'manage' },
  { to: '/callbacks', label: 'Буцаж залгах', icon: PhoneForwarded, hotkey: 'b', section: 'manage' },
  { to: '/lexicon', label: 'Lexicon', icon: BookA, hotkey: 'x', section: 'manage' },
  { to: '/settings', label: 'Тохиргоо', icon: Settings, hotkey: 's', section: 'system' },
  { to: '/admin', label: 'Платформ админ', icon: ShieldCheck, hotkey: 'm', section: 'system', platformAdmin: true },
]

/** Nav items visible to a user (platform-admin entries only for platform admins). */
export function visibleNavItems(isPlatformAdmin: boolean): NavItem[] {
  return NAV_ITEMS.filter((n) => !n.platformAdmin || isPlatformAdmin)
}

export const NAV_SECTIONS: { id: NavItem['section']; label: string }[] = [
  { id: 'main', label: 'Үндсэн' },
  { id: 'manage', label: 'Удирдлага' },
  { id: 'system', label: 'Систем' },
]

/** Nav item that owns `pathname` (longest prefix match; "/" only matches exactly). */
export function navItemFor(pathname: string): NavItem | undefined {
  if (pathname === '/' || pathname === '') return NAV_ITEMS[0]
  return NAV_ITEMS.filter((n) => n.to !== '/' && (pathname === n.to || pathname.startsWith(n.to + '/')))
    .sort((a, b) => b.to.length - a.to.length)[0]
}
