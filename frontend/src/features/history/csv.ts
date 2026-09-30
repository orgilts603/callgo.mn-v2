import type { Call } from '@/lib/types'

export function csvEscape(v: string | number | undefined | null): string {
  const s = v === undefined || v === null ? '' : String(v)
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export function callsToCsv(calls: Call[], campaignNames: Record<string, string> = {}): string {
  const header = ['id', 'startedAt', 'direction', 'from', 'to', 'campaign', 'status', 'durationSec', 'sentiment', 'intent', 'summary', 'recordingUrl']
  const rows = calls.map((c) => [
    c.id, c.startedAt, c.direction, c.fromNumber, c.toNumber, c.campaignId ? (campaignNames[c.campaignId] ?? c.campaignId) : '',
    c.status, c.durationSec, c.sentiment ?? '', c.intent ?? '', c.summary ?? '', c.recordingUrl ?? '',
  ])
  return [header, ...rows].map((r) => r.map(csvEscape).join(',')).join('\r\n')
}

export function downloadCsv(filename: string, content: string) {
  const blob = new Blob(['﻿' + content], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
