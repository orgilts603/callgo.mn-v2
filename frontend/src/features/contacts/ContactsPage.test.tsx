import { describe, expect, it, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

const get = vi.fn()
const post = vi.fn()
vi.mock('@/lib/api', () => ({ api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a), delete: vi.fn() } }))

import { ContactsPage } from './ContactsPage'
import { normalizePhone, parseTags } from './phone'

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><MemoryRouter><ContactsPage /></MemoryRouter></QueryClientProvider>)
}

describe('phone helpers', () => {
  it('normalizes and validates', () => {
    expect(normalizePhone('9911 2233')).toBe('+97699112233')
    expect(normalizePhone('+976 9911-2233')).toBe('+97699112233')
    expect(normalizePhone('99112')).toBeNull()
    expect(normalizePhone('+97699112')).toBeNull()
    expect(normalizePhone('+14155550123')).toBe('+14155550123')
    expect(parseTags(' vip, шинэ ,,vip')).toEqual(['vip', 'шинэ'])
  })
})

describe('ContactsPage', () => {
  beforeEach(() => {
    get.mockReset(); post.mockReset()
    get.mockResolvedValue({ items: [], total: 0 })
    post.mockResolvedValue({ contact: {} })
  })

  it('new dialog posts the body', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /Шинэ харилцагч/ }))
    fireEvent.change(screen.getByPlaceholderText('+976 9911 2233'), { target: { value: '99112233' } })
    fireEvent.change(screen.getByPlaceholderText('Бат-Эрдэнэ'), { target: { value: 'Бат' } })
    fireEvent.change(screen.getByPlaceholderText('vip, шинэ'), { target: { value: 'vip, шинэ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/contacts', { phone: '+97699112233', name: 'Бат', tags: ['vip', 'шинэ'] }))
  })

  it('rejects invalid phone', async () => {
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /Шинэ харилцагч/ }))
    fireEvent.change(screen.getByPlaceholderText('+976 9911 2233'), { target: { value: '123' } })
    fireEvent.click(screen.getByRole('button', { name: 'Хадгалах' }))
    expect(await screen.findByText('Утасны дугаар буруу байна')).toBeInTheDocument()
    expect(post).not.toHaveBeenCalled()
  })
})
