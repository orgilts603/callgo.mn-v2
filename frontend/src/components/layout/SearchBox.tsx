import { forwardRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Search } from 'lucide-react'
import { cn } from '@/lib/utils'

const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)

/** Command-palette style search. Enter searches call history (/calls?q=…). */
export const SearchBox = forwardRef<HTMLInputElement, { className?: string }>(({ className }, ref) => {
  const [q, setQ] = useState('')
  const navigate = useNavigate()
  return (
    <form
      role="search"
      className={cn('group relative', className)}
      onSubmit={(e) => {
        e.preventDefault()
        const v = q.trim()
        if (!v) return
        navigate(`/calls?q=${encodeURIComponent(v)}`)
        setQ('')
        ;(e.currentTarget.querySelector('input') as HTMLInputElement | null)?.blur()
      }}
    >
      <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-[var(--fg-subtle)]" aria-hidden />
      <input
        ref={ref}
        value={q}
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Escape') { setQ(''); e.currentTarget.blur() } }}
        type="search"
        aria-label="Хайх"
        placeholder="Дугаар, харилцагч хайх…"
        className="h-8 w-full rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-inset)] pl-8 pr-14 text-[13px] text-[var(--fg)] placeholder:text-[var(--fg-subtle)] transition-colors hover:border-[var(--border)] focus:border-[var(--accent)] focus:outline-none focus-visible:outline-none focus:ring-2 focus:ring-[var(--accent-soft)] [&::-webkit-search-cancel-button]:hidden"
      />
      <kbd className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 group-focus-within:opacity-0">{isMac ? '⌘' : 'Ctrl'} K</kbd>
    </form>
  )
})
SearchBox.displayName = 'SearchBox'
