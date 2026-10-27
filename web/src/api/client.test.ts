import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from './client'
import { CSRF_COOKIE, clearCsrfToken } from './csrf'

function jsonResponse(payload: unknown, status = 200): Response {
  return {
    status,
    ok: status >= 200 && status < 300,
    text: async () => JSON.stringify(payload),
  } as unknown as Response
}

function clearCookie(): void {
  document.cookie = `${CSRF_COOKIE}=; path=/; max-age=0`
}

describe('api client CSRF contract', () => {
  const fetchMock = vi.fn()

  beforeEach(() => {
    clearCookie()
    clearCsrfToken()
    fetchMock.mockReset()
    fetchMock.mockResolvedValue(jsonResponse({ ok: true }))
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    clearCookie()
    clearCsrfToken()
    vi.unstubAllGlobals()
  })

  it('echoes the csrf cookie in X-CSRF-Token on POST/PATCH/DELETE', async () => {
    document.cookie = `${CSRF_COOKIE}=token-from-cookie; path=/`

    await api.post('/api/console/keys', { name: 'prod' })
    await api.patch('/api/console/keys/1', { status: 'DISABLED' })
    await api.delete('/api/console/keys/1', { confirm: true })

    for (const call of fetchMock.mock.calls) {
      const headers = call[1].headers as Headers
      expect(headers.get('X-CSRF-Token')).toBe('token-from-cookie')
    }
  })

  it('does not send the header on GET', async () => {
    document.cookie = `${CSRF_COOKIE}=token-from-cookie; path=/`

    await api.get('/api/console/overview')

    const headers = fetchMock.mock.calls[0][1].headers as Headers
    expect(headers.get('X-CSRF-Token')).toBeNull()
  })

  it('remembers csrf_token from a response body when the cookie is absent', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ user: {}, csrf_token: 'token-from-body' }))

    await api.post('/api/v1/auth/login', { identifier: 'a@b.c', password: 'x' })
    await api.post('/api/console/keys', { name: 'prod' })

    const headers = fetchMock.mock.calls[1][1].headers as Headers
    expect(headers.get('X-CSRF-Token')).toBe('token-from-body')
  })

  it('sends the confirm body the DELETE key endpoint requires', async () => {
    await api.delete('/api/console/keys/9', { confirm: true })

    const body = fetchMock.mock.calls[0][1].body as string
    expect(JSON.parse(body)).toEqual({ confirm: true })
  })
})
