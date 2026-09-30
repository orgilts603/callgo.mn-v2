import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useUI } from '@/app/ui'
import { NAV_ITEMS } from './nav'

export function isTypingTarget(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false
  return t.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(t.tagName)
}

/**
 * Global shell shortcuts:
 *  - "[" toggles the sidebar
 *  - "g" then a nav key (d, l, c, k, p, x, s) navigates
 *  - ⌘K / Ctrl+K or "/" focuses the search box (handled via `onSearch`)
 */
export function useShellHotkeys(onSearch: () => void) {
  const navigate = useNavigate()
  useEffect(() => {
    let chordAt = 0
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); onSearch(); return }
      if (e.metaKey || e.ctrlKey || e.altKey || isTypingTarget(e.target)) return
      if (document.querySelector('[aria-modal="true"]')) return
      if (e.key === '/') { e.preventDefault(); onSearch(); return }
      if (e.key === '[') { useUI.getState().toggleSidebar(); return }
      if (e.key === 'g') { chordAt = Date.now(); return }
      if (chordAt && Date.now() - chordAt < 1200) {
        chordAt = 0
        const item = NAV_ITEMS.find((n) => n.hotkey === e.key.toLowerCase())
        if (item) { e.preventDefault(); navigate(item.to) }
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navigate, onSearch])
}
