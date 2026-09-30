import { Coins } from 'lucide-react'
import type { CallUsage } from '@/lib/types'

const nf = (n: number) => new Intl.NumberFormat('en-US').format(Math.round(n)).replace(/,/g, ' ')

/** Per-call cost + provider usage under the summary ("Зардал: 120 ₮ · LLM 1 200/300 токен · STT 45 сек · TTS 800 тэмдэгт"). */
export function UsageLine({ usage }: { usage?: CallUsage | null }) {
  if (!usage) return null
  const parts = [
    `LLM ${nf(usage.llmTokensIn)}/${nf(usage.llmTokensOut)} токен`,
    `STT ${nf(usage.sttSeconds)} сек`,
    `TTS ${nf(usage.ttsChars)} тэмдэгт`,
  ]
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-1 text-xs text-[var(--fg-muted)]" data-testid="usage-line">
      <Coins className="h-3.5 w-3.5 text-[var(--fg-subtle)]" aria-hidden />
      <span>Зардал: <span className="tabular font-medium text-[var(--fg)]">{nf(usage.costMnt)} ₮</span></span>
      {parts.map((p) => <span key={p} className="text-[var(--fg-subtle)]">· {p}</span>)}
      {usage.llmModel && <span className="font-mono text-[11px] text-[var(--fg-subtle)]">· {usage.llmModel}</span>}
    </div>
  )
}
