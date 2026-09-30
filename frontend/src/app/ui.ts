// Per-browser UI preferences (sidebar collapse, colour theme), persisted in localStorage.
import { create } from 'zustand'

export type Theme = 'dark' | 'light'
const SIDEBAR_KEY = 'callgo.sidebar.collapsed'
const THEME_KEY = 'callgo.theme'

function read(key: string): string | null {
  try { return localStorage.getItem(key) } catch { return null }
}
function write(key: string, value: string) {
  try { localStorage.setItem(key, value) } catch { /* private mode */ }
}
export function applyTheme(theme: Theme) {
  if (typeof document === 'undefined') return
  if (theme === 'light') document.documentElement.dataset.theme = 'light'
  else delete document.documentElement.dataset.theme
}

interface UIState {
  sidebarCollapsed: boolean
  theme: Theme
  toggleSidebar: () => void
  setSidebarCollapsed: (v: boolean) => void
  setTheme: (t: Theme) => void
}

export const useUI = create<UIState>((set, get) => ({
  sidebarCollapsed: read(SIDEBAR_KEY) === '1',
  theme: read(THEME_KEY) === 'light' ? 'light' : 'dark',
  toggleSidebar: () => get().setSidebarCollapsed(!get().sidebarCollapsed),
  setSidebarCollapsed: (v) => { write(SIDEBAR_KEY, v ? '1' : '0'); set({ sidebarCollapsed: v }) },
  setTheme: (t) => { write(THEME_KEY, t); applyTheme(t); set({ theme: t }) },
}))
