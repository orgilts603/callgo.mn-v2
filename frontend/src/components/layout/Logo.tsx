import { cn } from '@/lib/utils'

/** CallGo mark: rounded tile with a voice-wave glyph. */
export function LogoMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" className={cn('h-7 w-7 shrink-0', className)} aria-hidden>
      <rect width="32" height="32" rx="8" fill="var(--surface-3)" />
      <rect width="32" height="32" rx="8" fill="url(#callgo-mark-shade)" />
      <rect x="0.5" y="0.5" width="31" height="31" rx="7.5" fill="none" stroke="var(--border-strong)" />
      <g stroke="var(--fg)" strokeWidth="2.4" strokeLinecap="round">
        <path d="M9 13.5v5" />
        <path d="M13.5 10v12" />
        <path d="M18 12.5v7" />
        <path d="M22.5 14.5v3" opacity=".7" />
      </g>
      <defs>
        <linearGradient id="callgo-mark-shade" x1="0" y1="0" x2="32" y2="32" gradientUnits="userSpaceOnUse">
          <stop stopColor="#fff" stopOpacity=".12" />
          <stop offset="1" stopColor="#000" stopOpacity=".35" />
        </linearGradient>
      </defs>
    </svg>
  )
}

export function Logo({ className, withText = true }: { className?: string; withText?: boolean }) {
  return (
    <span className={cn('inline-flex items-center gap-2.5', className)}>
      <LogoMark />
      {withText && (
        <span className="text-[15px] font-semibold tracking-[-0.02em] text-[var(--fg)]">
          CallGo<span className="text-[var(--fg-subtle)]">.mn</span>
        </span>
      )}
    </span>
  )
}
