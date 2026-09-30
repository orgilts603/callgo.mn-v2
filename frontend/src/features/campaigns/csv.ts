import Papa from 'papaparse'

// Same header synonyms as the backend CSV importer.
export const PHONE_SYNONYMS = ['phone', 'утас', 'дугаар', 'mobile', 'tel']
export const NAME_SYNONYMS = ['name', 'нэр']

export interface CsvPreview {
  columns: string[]
  phoneColumn: string | null
  nameColumn: string | null
  rows: Record<string, string>[]
  previewRows: Record<string, string>[]
  rowCount: number
}

const norm = (s: string) => s.replace(/^﻿/, '').trim().toLowerCase()

export function findColumn(columns: string[], synonyms: string[]): string | null {
  return columns.find((c) => synonyms.includes(norm(c))) ?? null
}

export function detectColumns(columns: string[]): { phoneColumn: string | null; nameColumn: string | null } {
  return { phoneColumn: findColumn(columns, PHONE_SYNONYMS), nameColumn: findColumn(columns, NAME_SYNONYMS) }
}

export function parseCsvText(text: string, previewSize = 5): CsvPreview {
  const res = Papa.parse<Record<string, string>>(text.replace(/^﻿/, ''), {
    header: true,
    skipEmptyLines: 'greedy',
    transformHeader: (h) => h.trim(),
  })
  const columns = (res.meta.fields ?? []).filter((c) => c !== '')
  const rows = res.data
  return { columns, ...detectColumns(columns), rows, previewRows: rows.slice(0, previewSize), rowCount: rows.length }
}

export function readFileText(file: File): Promise<string> {
  if (typeof file.text === 'function') return file.text()
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result ?? ''))
    r.onerror = () => reject(r.error)
    r.readAsText(file)
  })
}

export async function parseCsvFile(file: File, previewSize = 5): Promise<CsvPreview> {
  return parseCsvText(await readFileText(file), previewSize)
}

export const TEMPLATE_CSV = '﻿phone,name,note\r\n+97699112233,Бат,Жишээ тэмдэглэл\r\n+97688001122,Сараа,\r\n'

export function downloadTemplate() {
  const blob = new Blob([TEMPLATE_CSV], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'campaign-template.csv'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
