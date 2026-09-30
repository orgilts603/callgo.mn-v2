import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { BadgeCheck, ChevronDown, LogOut, MailWarning, Moon, Settings, Sun } from 'lucide-react'
import { toast } from 'sonner'
import { resendVerification, roleLabel, useAuth } from '@/app/auth'
import { useUI } from '@/app/ui'
import { cn } from '@/lib/utils'

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  return (parts.length > 1 ? parts[0][0] + parts[1][0] : name.slice(0, 2)).toUpperCase() || '?'
}

const itemCls = 'flex w-full items-center gap-2 rounded-[var(--radius-sm)] px-2 h-8 text-[13px] text-[var(--fg-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)] focus-visible:bg-[var(--surface-2)] focus-visible:text-[var(--fg)] focus-visible:outline-none'

export function UserMenu() {
  const user = useAuth((s) => s.user)
  const org = useAuth((s) => s.org)
  const logout = useAuth((s) => s.logout)
  const theme = useUI((s) => s.theme)
  const setTheme = useUI((s) => s.setTheme)
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const menu = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => { if (!root.current?.contains(e.target as Node)) setOpen(false) }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') { setOpen(false); root.current?.querySelector<HTMLButtonElement>('button')?.focus(); return }
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
      e.preventDefault()
      const items = Array.from(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])
      const idx = items.indexOf(document.activeElement as HTMLElement)
      const next = e.key === 'ArrowDown' ? (idx + 1) % items.length : (idx - 1 + items.length) % items.length
      items[next]?.focus()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    menu.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus()
    return () => { document.removeEventListener('mousedown', onDown); document.removeEventListener('keydown', onKey) }
  }, [open])

  const name = user?.name || user?.email || 'Хэрэглэгч'
  const unverified = !!user && !user.emailVerifiedAt
  const resend = () => {
    setOpen(false)
    resendVerification()
      .then(() => toast.success('Баталгаажуулах и-мэйлийг дахин илгээлээ.'))
      .catch(() => toast.error('И-мэйл илгээж чадсангүй. Дараа дахин оролдоно уу.'))
  }
  return (
    <div ref={root} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Хэрэглэгчийн цэс"
        className="flex h-8 items-center gap-2 rounded-[var(--radius-sm)] pl-1 pr-1.5 transition-colors hover:bg-[var(--surface-2)]"
      >
        <span className="relative flex h-6 w-6 items-center justify-center rounded-full bg-[var(--accent-soft)] text-[10.5px] font-semibold text-[var(--accent-fg)] ring-1 ring-[var(--accent-border)]">
          {initials(name)}
          {unverified && (
            <span data-testid="unverified-dot" title="И-мэйл баталгаажаагүй"
              className="absolute -right-0.5 -top-0.5 h-2 w-2 rounded-full bg-[var(--warning)] ring-2 ring-[var(--surface-0)]" />
          )}
        </span>
        <span className="hidden max-w-[140px] text-left leading-tight lg:block">
          <span className="block truncate text-[12.5px] font-medium text-[var(--fg)]">{name}</span>
          <span className="block truncate text-[11px] text-[var(--fg-subtle)]">{roleLabel(user?.role)}</span>
        </span>
        <ChevronDown className="h-3.5 w-3.5 text-[var(--fg-subtle)]" />
      </button>

      {open && (
        <div ref={menu} role="menu" aria-label="Хэрэглэгч"
          className="animate-pop-in absolute right-0 top-full z-50 mt-1.5 w-64 rounded-[var(--radius)] border border-[var(--border)] bg-[var(--surface-overlay)] p-1 shadow-[var(--shadow-lg)]">
          <div className="px-2 py-2">
            <div className="truncate text-[13px] font-medium text-[var(--fg)]">{name}</div>
            {user?.email && <div className="truncate text-xs text-[var(--fg-muted)]">{user.email}</div>}
            <div className="mt-1.5 flex items-center gap-1.5 text-[11px] text-[var(--fg-subtle)]">
              <span className="rounded border border-[var(--border)] px-1.5 py-px">{roleLabel(user?.role) || '—'}</span>
              {org && <span className="truncate">{org.name}</span>}
            </div>
            {user && (unverified ? (
              <div className="mt-2 flex items-center gap-1.5 text-[11px] text-[var(--warning-fg)]">
                <MailWarning className="h-3.5 w-3.5" /> И-мэйл баталгаажаагүй
              </div>
            ) : (
              <div className="mt-2 flex items-center gap-1.5 text-[11px] text-[var(--success-fg)]">
                <BadgeCheck className="h-3.5 w-3.5" /> И-мэйл баталгаажсан
              </div>
            ))}
          </div>
          <div className="my-1 border-t border-[var(--border-subtle)]" />
          {unverified && (
            <button type="button" role="menuitem" className={cn(itemCls, 'text-[var(--warning-fg)]')} onClick={resend}>
              <MailWarning className="h-4 w-4" /> И-мэйл баталгаажуулах
            </button>
          )}
          <button type="button" role="menuitem" className={itemCls} onClick={() => { setOpen(false); navigate('/settings') }}>
            <Settings className="h-4 w-4" /> Тохиргоо
          </button>
          <button type="button" role="menuitem" className={itemCls} onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}>
            {theme === 'dark' ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
            {theme === 'dark' ? 'Цайвар горим' : 'Бараан горим'}
          </button>
          <div className="my-1 border-t border-[var(--border-subtle)]" />
          <button type="button" role="menuitem" className={cn(itemCls, 'hover:text-[var(--danger-fg)]')} onClick={() => { setOpen(false); logout() }}>
            <LogOut className="h-4 w-4" /> Гарах
          </button>
        </div>
      )}
    </div>
  )
}
