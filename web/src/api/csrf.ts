/**
 * CSRF double-submit token for the console.
 *
 * The control plane sets a readable `csrf` cookie (not HttpOnly) on login/me
 * and rejects every session-authenticated POST/PATCH/PUT/DELETE under /api/
 * whose `X-CSRF-Token` header does not match both the cookie and the token it
 * derives from the session id (internal/control/mw/csrf.go).
 *
 * The cookie is the primary source because it is what the browser sends back;
 * the body/header copy in login & me responses is a fallback for the one tab
 * that hydrated before the cookie was written. Neither copy is persisted by
 * the SPA.
 */

export const CSRF_COOKIE = 'csrf'
export const CSRF_HEADER = 'X-CSRF-Token'

let memoToken = ''

/** Reads one cookie value without pulling in a dependency. */
export function readCookie(name: string): string {
  if (typeof document === 'undefined') return ''
  const prefix = `${name}=`
  for (const part of document.cookie.split(';')) {
    const entry = part.trim()
    if (entry.startsWith(prefix)) {
      try {
        return decodeURIComponent(entry.slice(prefix.length))
      } catch {
        return entry.slice(prefix.length)
      }
    }
  }
  return ''
}

/** Remembers a token returned by login/register/me. */
export function rememberCsrfToken(token: unknown): void {
  if (typeof token === 'string' && token.trim() !== '') memoToken = token.trim()
}

export function clearCsrfToken(): void {
  memoToken = ''
}

/** Cookie first (it dies with the session), remembered token second. */
export function getCsrfToken(): string {
  const fromCookie = readCookie(CSRF_COOKIE)
  if (fromCookie) {
    memoToken = fromCookie
    return fromCookie
  }
  return memoToken
}

/** True for methods the backend does not CSRF-check. */
export function isSafeMethod(method: string): boolean {
  const upper = method.toUpperCase()
  return upper === 'GET' || upper === 'HEAD' || upper === 'OPTIONS'
}
