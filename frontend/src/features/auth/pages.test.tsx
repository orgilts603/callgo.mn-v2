import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, getRefreshToken, getToken, HttpError } from '@/lib/api'
import { useAuth } from '@/app/auth'
import { renderRoutes, resetAuth, signIn, stubLive, testOrg, testUser } from '@/app/testing'
import LoginPage from './LoginPage'
import SignupPage from './SignupPage'
import VerifyEmailPage from './VerifyEmailPage'
import ForgotPasswordPage from './ForgotPasswordPage'
import ResetPasswordPage from './ResetPasswordPage'
import AcceptInvitationPage from './AcceptInvitationPage'
import { VerifyBanner } from './VerifyBanner'
import { passwordScore } from './AuthLayout'

const routes = [
  { path: '/login', element: <LoginPage /> },
  { path: '/signup', element: <SignupPage /> },
  { path: '/verify-email', element: <VerifyEmailPage /> },
  { path: '/forgot-password', element: <ForgotPasswordPage /> },
  { path: '/reset-password', element: <ResetPasswordPage /> },
  { path: '/accept-invitation', element: <AcceptInvitationPage /> },
  { path: '/', element: <div>HOME<VerifyBanner /></div> },
]
const authRes = { token: 'jwt-1', refreshToken: 'ref-1', user: testUser, org: testOrg }
const type = (label: string | RegExp, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })

beforeEach(() => { resetAuth(); sessionStorage.clear(); stubLive() })
afterEach(() => vi.restoreAllMocks())

describe('LoginPage links', () => {
  it('links to signup and forgot password', () => {
    renderRoutes(routes, { path: '/login' })
    expect(screen.getByRole('link', { name: 'Бүртгүүлэх' })).toHaveAttribute('href', '/signup')
    expect(screen.getByRole('link', { name: 'Нууц үг мартсан?' })).toHaveAttribute('href', '/forgot-password')
  })

  it('stores the refresh token on login', async () => {
    vi.spyOn(api, 'post').mockResolvedValue(authRes)
    renderRoutes(routes, { path: '/login' })
    type('И-мэйл', 'a@b.mn'); type('Нууц үг', 'secret123')
    fireEvent.click(screen.getByRole('button', { name: /Нэвтрэх/ }))
    await screen.findByText('HOME')
    expect(getRefreshToken()).toBe('ref-1')
  })
})

describe('SignupPage', () => {
  function fill() {
    type('Байгууллагын нэр', 'Acme ХХК')
    type('Таны нэр', 'Бат')
    type('И-мэйл', 'bat@acme.mn')
    type('Нууц үг', 'Secret123!')
    type('Утас (заавал биш)', '+97699112233')
  }

  it('creates the account and lands on the dashboard with the verify banner', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ ...authRes, user: { ...testUser, emailVerifiedAt: null } })
    renderRoutes(routes, { path: '/signup' })
    fill()
    fireEvent.click(screen.getByLabelText(/Үйлчилгээний нөхцөл/))
    fireEvent.click(screen.getByRole('button', { name: /Бүртгүүлэх/ }))
    await screen.findByText('HOME')
    expect(post).toHaveBeenCalledWith('/auth/signup', { orgName: 'Acme ХХК', name: 'Бат', email: 'bat@acme.mn', password: 'Secret123!', phone: '+97699112233' })
    expect(getToken()).toBe('jwt-1')
    expect(getRefreshToken()).toBe('ref-1')
    expect(screen.getByRole('region', { name: 'И-мэйл баталгаажуулалт' })).toHaveTextContent('И-мэйлээ баталгаажуулна уу')
  })

  it('requires the terms checkbox and a long enough password before calling the API', async () => {
    const post = vi.spyOn(api, 'post')
    renderRoutes(routes, { path: '/signup' })
    fill()
    type('Нууц үг', 'short')
    fireEvent.click(screen.getByRole('button', { name: /Бүртгүүлэх/ }))
    const alerts = await screen.findAllByRole('alert')
    expect(alerts.map((a) => a.textContent).join(' ')).toMatch(/8 тэмдэгт/)
    expect(alerts.map((a) => a.textContent).join(' ')).toMatch(/нөхцөлийг зөвшөөрнө үү/)
    expect(post).not.toHaveBeenCalled()
  })

  it('shows a friendly error when the email is taken', async () => {
    vi.spyOn(api, 'post').mockRejectedValue(new HttpError(409, 'conflict', 'email exists'))
    renderRoutes(routes, { path: '/signup' })
    fill()
    fireEvent.click(screen.getByLabelText(/Үйлчилгээний нөхцөл/))
    fireEvent.click(screen.getByRole('button', { name: /Бүртгүүлэх/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('бүртгэл үүссэн байна')
    expect(getToken()).toBeNull()
  })

  it('rates password strength', () => {
    expect(passwordScore('')).toBe(0)
    expect(passwordScore('abc')).toBe(1)
    expect(passwordScore('abcdefgh')).toBe(1)
    expect(passwordScore('abcdefg1')).toBe(2)
    expect(passwordScore('Abcdefg1')).toBe(3)
    expect(passwordScore('Abcdefg1!xyz')).toBe(4)
  })
})

describe('VerifyEmailPage', () => {
  it('verifies the token once and updates the signed-in user', async () => {
    signIn()
    useAuth.setState({ user: { ...testUser, emailVerifiedAt: null } })
    const verified = { ...testUser, emailVerifiedAt: '2026-09-30T00:00:00Z' }
    const post = vi.spyOn(api, 'post').mockResolvedValue({ user: verified })
    renderRoutes(routes, { path: '/verify-email?token=tok-1' })
    expect(await screen.findByText('И-мэйл баталгаажлаа')).toBeInTheDocument()
    expect(post).toHaveBeenCalledTimes(1)
    expect(post).toHaveBeenCalledWith('/auth/verify-email', { token: 'tok-1' })
    expect(useAuth.getState().user?.emailVerifiedAt).toBe('2026-09-30T00:00:00Z')
    fireEvent.click(screen.getByRole('button', { name: 'Үргэлжлүүлэх' }))
    expect(await screen.findByText('HOME')).toBeInTheDocument()
  })

  it('shows the failed state for an expired token and offers to resend when signed in', async () => {
    signIn()
    const post = vi.spyOn(api, 'post').mockImplementation(async (path: string) => {
      if (path === '/auth/verify-email') throw new HttpError(400, 'invalid', 'expired')
      return undefined
    })
    renderRoutes(routes, { path: '/verify-email?token=bad' })
    expect(await screen.findByRole('alert')).toHaveTextContent('хүчингүй эсвэл хугацаа нь дууссан')
    fireEvent.click(screen.getByRole('button', { name: 'Дахин илгээх' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/auth/resend-verification'))
  })

  it('handles a missing token without calling the API', async () => {
    const post = vi.spyOn(api, 'post')
    renderRoutes(routes, { path: '/verify-email' })
    expect(await screen.findByRole('alert')).toHaveTextContent('токен олдсонгүй')
    expect(post).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: 'Дахин илгээх' })).toBeNull()
  })
})

describe('ForgotPasswordPage', () => {
  it('always shows the neutral confirmation after sending', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue(undefined)
    renderRoutes(routes, { path: '/forgot-password' })
    type('И-мэйл', ' bat@acme.mn ')
    fireEvent.click(screen.getByRole('button', { name: 'Сэргээх холбоос илгээх' }))
    expect(await screen.findByText('И-мэйлээ шалгана уу')).toBeInTheDocument()
    expect(post).toHaveBeenCalledWith('/auth/forgot-password', { email: 'bat@acme.mn' })
  })

  it('validates the email and surfaces rate limiting', async () => {
    const post = vi.spyOn(api, 'post').mockRejectedValue(new HttpError(429, 'rate_limited', 'slow down'))
    renderRoutes(routes, { path: '/forgot-password' })
    type('И-мэйл', 'nope')
    fireEvent.click(screen.getByRole('button', { name: 'Сэргээх холбоос илгээх' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Зөв и-мэйл')
    expect(post).not.toHaveBeenCalled()
    type('И-мэйл', 'bat@acme.mn')
    fireEvent.click(screen.getByRole('button', { name: 'Сэргээх холбоос илгээх' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Хэт олон оролдлого'))
  })
})

describe('ResetPasswordPage', () => {
  it('resets the password with the URL token', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue(undefined)
    renderRoutes(routes, { path: '/reset-password?token=rst' })
    type('Шинэ нууц үг', 'NewSecret1')
    type('Нууц үг давтах', 'NewSecret1')
    fireEvent.click(screen.getByRole('button', { name: 'Нууц үг шинэчлэх' }))
    expect(await screen.findByText('Нууц үг шинэчлэгдлээ')).toBeInTheDocument()
    expect(post).toHaveBeenCalledWith('/auth/reset-password', { token: 'rst', password: 'NewSecret1' })
    fireEvent.click(screen.getByRole('button', { name: 'Нэвтрэх' }))
    expect(await screen.findByLabelText('И-мэйл')).toBeInTheDocument()
  })

  it('rejects mismatched passwords locally and expired links from the API', async () => {
    const post = vi.spyOn(api, 'post').mockRejectedValue(new HttpError(410, 'invalid', 'used'))
    renderRoutes(routes, { path: '/reset-password?token=rst' })
    type('Шинэ нууц үг', 'NewSecret1')
    type('Нууц үг давтах', 'Different1')
    fireEvent.click(screen.getByRole('button', { name: 'Нууц үг шинэчлэх' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('таарахгүй')
    expect(post).not.toHaveBeenCalled()
    type('Нууц үг давтах', 'NewSecret1')
    fireEvent.click(screen.getByRole('button', { name: 'Нууц үг шинэчлэх' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('хүчингүй эсвэл хугацаа нь дууссан'))
  })

  it('explains a link without a token', () => {
    renderRoutes(routes, { path: '/reset-password' })
    expect(screen.getByRole('alert')).toHaveTextContent('токен олдсонгүй')
    expect(screen.queryByLabelText('Шинэ нууц үг')).toBeNull()
  })
})

describe('AcceptInvitationPage', () => {
  it('accepts the invitation, stores both tokens and opens the dashboard', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue(authRes)
    renderRoutes(routes, { path: '/accept-invitation?token=inv-1' })
    type('Таны нэр', 'Дорж')
    type('Нууц үг', 'Secret123')
    type('Нууц үг давтах', 'Secret123')
    fireEvent.click(screen.getByRole('button', { name: /Нэгдэх/ }))
    await screen.findByText('HOME')
    expect(post).toHaveBeenCalledWith('/auth/accept-invitation', { token: 'inv-1', name: 'Дорж', password: 'Secret123' })
    expect(getToken()).toBe('jwt-1')
    expect(getRefreshToken()).toBe('ref-1')
  })

  it('shows an invalid-link error for an expired invitation', async () => {
    vi.spyOn(api, 'post').mockRejectedValue(new HttpError(404, 'not_found', 'no invitation'))
    renderRoutes(routes, { path: '/accept-invitation?token=old' })
    type('Таны нэр', 'Дорж')
    type('Нууц үг', 'Secret123')
    type('Нууц үг давтах', 'Secret123')
    fireEvent.click(screen.getByRole('button', { name: /Нэгдэх/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Урилгын холбоос хүчингүй')
    expect(getToken()).toBeNull()
  })
})

describe('VerifyBanner', () => {
  it('is hidden for verified users', () => {
    signIn()
    useAuth.setState({ user: { ...testUser, emailVerifiedAt: '2026-09-01T00:00:00Z' } })
    renderRoutes(routes, { path: '/' })
    expect(screen.queryByRole('region', { name: 'И-мэйл баталгаажуулалт' })).toBeNull()
  })

  it('resends the verification email and can be dismissed for the session', async () => {
    signIn()
    useAuth.setState({ user: { ...testUser, emailVerifiedAt: null } })
    const post = vi.spyOn(api, 'post').mockResolvedValue(undefined)
    const { unmount } = renderRoutes(routes, { path: '/' })
    fireEvent.click(screen.getByRole('button', { name: 'Дахин илгээх' }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/auth/resend-verification'))
    expect(await screen.findByRole('button', { name: 'Илгээлээ' })).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Хаах' }))
    expect(screen.queryByRole('region', { name: 'И-мэйл баталгаажуулалт' })).toBeNull()
    unmount()
    act(() => { renderRoutes(routes, { path: '/' }) })
    expect(screen.queryByRole('region', { name: 'И-мэйл баталгаажуулалт' })).toBeNull()
  })
})
