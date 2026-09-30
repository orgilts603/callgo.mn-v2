import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ConfirmDialog } from './index'

describe('ConfirmDialog', () => {
  it('runs an async confirm, then closes', async () => {
    const onClose = vi.fn()
    let resolve!: () => void
    const onConfirm = vi.fn(() => new Promise<void>((r) => { resolve = r }))
    render(<ConfirmDialog open onClose={onClose} onConfirm={onConfirm} title="Устгах уу?" description="Буцаах боломжгүй." confirmLabel="Устгах" />)
    expect(screen.getByRole('dialog')).toHaveAccessibleName(/Устгах уу\?/)
    fireEvent.click(screen.getByRole('button', { name: 'Устгах' }))
    expect(onConfirm).toHaveBeenCalledOnce()
    expect(screen.getByRole('button', { name: 'Устгах' })).toBeDisabled()
    resolve()
    await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
  })

  it('stays open when confirm rejects and cancels on "Болих"', async () => {
    const onClose = vi.fn()
    render(<ConfirmDialog open onClose={onClose} onConfirm={() => Promise.reject(new Error('x'))} title="Итгэлтэй байна уу?" tone="primary" />)
    fireEvent.click(screen.getByRole('button', { name: 'Баталгаажуулах' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Баталгаажуулах' })).not.toBeDisabled())
    expect(onClose).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Болих' }))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('renders nothing when closed', () => {
    render(<ConfirmDialog open={false} onClose={() => {}} onConfirm={() => {}} title="x" />)
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
