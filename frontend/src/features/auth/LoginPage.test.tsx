import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, getToken, HttpError } from '@/lib/api'
import { useAuth } from '@/app/auth'
import { renderRoutes, resetAuth, stubLive, testOrg, testUser } from '@/app/testing'
import LoginPage from './LoginPage'

const routes = [
  { path: '/login', element: <LoginPage /> },
  { path: '/', element: <div>HOME</div> },
]

describe('LoginPage', () => {
  beforeEach(() => { resetAuth(); stubLive() })
  afterEach(() => vi.restoreAllMocks())

  it('submits credentials, stores the token and redirects home', async () => {
    const post = vi.spyOn(api, 'post').mockResolvedValue({ token: 'jwt-123', user: testUser, org: testOrg })
    renderRoutes(routes, { path: '/login' })

    fireEvent.change(screen.getByLabelText('И-мэйл'), { target: { value: 'admin@callgo.mn' } })
    fireEvent.change(screen.getByLabelText('Нууц үг'), { target: { value: 'admin1234' } })
    fireEvent.click(screen.getByRole('button', { name: /Нэвтрэх/ }))

    await screen.findByText('HOME')
    expect(post).toHaveBeenCalledWith('/auth/login', { email: 'admin@callgo.mn', password: 'admin1234' })
    expect(getToken()).toBe('jwt-123')
    expect(useAuth.getState()).toMatchObject({ token: 'jwt-123', user: testUser, org: testOrg })
  })

  it('shows an error and keeps the user on the page on bad credentials', async () => {
    vi.spyOn(api, 'post').mockRejectedValue(new HttpError(401, 'unauthorized', 'bad'))
    renderRoutes(routes, { path: '/login' })

    fireEvent.change(screen.getByLabelText('И-мэйл'), { target: { value: 'x@y.mn' } })
    fireEvent.change(screen.getByLabelText('Нууц үг'), { target: { value: 'nope' } })
    fireEvent.click(screen.getByRole('button', { name: /Нэвтрэх/ }))

    expect(await screen.findByRole('alert')).toHaveTextContent('И-мэйл эсвэл нууц үг буруу байна.')
    expect(getToken()).toBeNull()
    expect(screen.queryByText('HOME')).not.toBeInTheDocument()
  })

  it('validates empty fields without calling the API', async () => {
    const post = vi.spyOn(api, 'post')
    renderRoutes(routes, { path: '/login' })
    fireEvent.click(screen.getByRole('button', { name: /Нэвтрэх/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('И-мэйл болон нууц үгээ оруулна уу.')
    expect(post).not.toHaveBeenCalled()
  })

  it('fills the demo credentials from the hint', async () => {
    renderRoutes(routes, { path: '/login' })
    fireEvent.click(screen.getByTitle('Демо мэдээллийг бөглөх'))
    await waitFor(() => expect(screen.getByLabelText('И-мэйл')).toHaveValue('admin@callgo.mn'))
    expect(screen.getByLabelText('Нууц үг')).toHaveValue('admin1234')
  })
})
