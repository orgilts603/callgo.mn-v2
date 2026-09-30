import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/lib/api'
import { useAuth } from '@/app/auth'
import { renderPage, resetAuth, signIn, stubLive, testUser } from '@/app/testing'
import { UserMenu } from './UserMenu'

describe('UserMenu email verification', () => {
  beforeEach(() => { resetAuth(); stubLive(); signIn() })
  afterEach(() => vi.restoreAllMocks())

  it('flags an unverified email and resends the verification link', async () => {
    useAuth.setState({ user: { ...testUser, emailVerifiedAt: null } })
    const post = vi.spyOn(api, 'post').mockResolvedValue(undefined)
    renderPage(<UserMenu />)
    expect(screen.getByTestId('unverified-dot')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Хэрэглэгчийн цэс' }))
    const menu = screen.getByRole('menu', { name: 'Хэрэглэгч' })
    expect(menu).toHaveTextContent('И-мэйл баталгаажаагүй')
    fireEvent.click(within(menu).getByRole('menuitem', { name: /И-мэйл баталгаажуулах/ }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/auth/resend-verification'))
  })

  it('shows a verified badge and no reminder once verified', () => {
    useAuth.setState({ user: { ...testUser, emailVerifiedAt: '2026-09-01T00:00:00Z' } })
    renderPage(<UserMenu />)
    expect(screen.queryByTestId('unverified-dot')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Хэрэглэгчийн цэс' }))
    const menu = screen.getByRole('menu', { name: 'Хэрэглэгч' })
    expect(menu).toHaveTextContent('И-мэйл баталгаажсан')
    expect(within(menu).queryByRole('menuitem', { name: /баталгаажуулах/ })).toBeNull()
  })
})
