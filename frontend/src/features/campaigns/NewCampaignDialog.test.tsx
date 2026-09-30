import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { AgentProfile, Campaign, SIPNumber } from '@/lib/types'
import { NewCampaignDialog } from './NewCampaignDialog'

const sips = [
  { id: 'sip-1', label: 'Үндсэн', number: '+97677001122', allowOutbound: true, active: true, agentProfileId: 'ap-1' },
  { id: 'sip-2', label: 'Inbound only', number: '+97677003344', allowOutbound: false, active: true },
  { id: 'sip-3', label: 'Disabled', number: '+97677005566', allowOutbound: true, active: false },
] as unknown as SIPNumber[]
const profiles = [{ id: 'ap-1', name: 'Борлуулагч' }, { id: 'ap-2', name: 'Сануулагч' }] as unknown as AgentProfile[]
const created = { id: 'c-new', name: 'Тест', total: 2, completed: 0, failed: 0 } as unknown as Campaign

function renderDialog() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter><NewCampaignDialog open onClose={() => {}} /></MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('NewCampaignDialog', () => {
  beforeEach(() => {
    vi.spyOn(api, 'get').mockImplementation(async (path: string) => {
      if (path === '/sip-numbers') return { items: sips } as never
      if (path === '/agent-profiles') return { items: profiles } as never
      return { items: [] } as never
    })
  })
  afterEach(() => { cleanup(); vi.restoreAllMocks() })

  it('only offers outbound-enabled active SIP numbers', async () => {
    renderDialog()
    await screen.findByRole('option', { name: /Үндсэн/ })
    expect(screen.queryByRole('option', { name: /Inbound only/ })).toBeNull()
    expect(screen.queryByRole('option', { name: /Disabled/ })).toBeNull()
  })

  it('walks the steps, previews CSV and posts multipart FormData', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      campaign: created, targets: { imported: 1, skipped: 1, errors: [{ row: 3, message: 'bad phone' }] },
    } as never)
    renderDialog()
    await screen.findByRole('option', { name: /Үндсэн/ })

    fireEvent.change(screen.getByPlaceholderText(/Долдугаар/), { target: { value: 'Тест' } })
    fireEvent.change(screen.getByPlaceholderText(/Сайн байна уу/), { target: { value: 'Сайн уу {{name}}' } })
    fireEvent.change(screen.getAllByRole('combobox')[0], { target: { value: 'sip-1' } })
    fireEvent.change(screen.getAllByRole('combobox')[1], { target: { value: 'ap-2' } })
    fireEvent.change(screen.getByLabelText(/Зэрэг дуудлага \(1-50\)/), { target: { value: '7' } })
    fireEvent.change(screen.getByLabelText(/Дахин оролдох/), { target: { value: '3' } })
    fireEvent.click(screen.getByRole('button', { name: 'Үргэлжлүүлэх' }))

    const file = new File(['﻿Утас,Нэр,x\n99112233,Бат,1\n88112233,Сараа,2\n'], 'list.csv', { type: 'text/csv' })
    fireEvent.change(screen.getByLabelText('CSV файл'), { target: { files: [file] } })
    expect(await screen.findByTestId('phone-detected')).toHaveTextContent('Утас')
    expect(screen.getByTestId('csv-preview')).toHaveTextContent('2 мөр')

    fireEvent.click(screen.getByRole('button', { name: 'Үүсгэх' }))
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    const [path, body] = post.mock.calls[0]
    expect(path).toBe('/campaigns')
    expect(body).toBeInstanceOf(FormData)
    const fd = body as FormData
    expect(fd.get('name')).toBe('Тест')
    expect(fd.get('script')).toBe('Сайн уу {{name}}')
    expect(fd.get('sipNumberId')).toBe('sip-1')
    expect(fd.get('agentProfileId')).toBe('ap-2')
    expect(fd.get('concurrency')).toBe('7')
    expect(fd.get('maxAttempts')).toBe('3')
    expect((fd.get('file') as File).name).toBe('list.csv')

    const result = await screen.findByTestId('result')
    expect(result).toHaveTextContent('Импортолсон')
    fireEvent.click(screen.getByText(/Алдааны жагсаалт/))
    expect(screen.getByText(/bad phone/)).toBeInTheDocument()
  })

  it('blocks submit when no phone column is found', async () => {
    renderDialog()
    await screen.findByRole('option', { name: /Үндсэн/ })
    fireEvent.change(screen.getByPlaceholderText(/Долдугаар/), { target: { value: 'X' } })
    fireEvent.change(screen.getAllByRole('combobox')[0], { target: { value: 'sip-1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Үргэлжлүүлэх' }))
    fireEvent.change(screen.getByLabelText('CSV файл'), { target: { files: [new File(['a,b\n1,2\n'], 'x.csv')] } })
    await screen.findByTestId('phone-missing')
    expect(screen.getByRole('button', { name: 'Үүсгэх' })).toBeDisabled()
  })
})
