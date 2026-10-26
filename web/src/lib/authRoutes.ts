import { isAdminRole, ROUTES } from './constants'
import type { UserRole } from './constants'

/** PRD §4.4: personal accounts land in the console, admin roles in /admin. */
export function postLoginPath(role: UserRole | null | undefined): string {
  return isAdminRole(role) ? ROUTES.admin : ROUTES.console
}

/**
 * Honours the guard's `next` parameter only for same-origin absolute paths.
 * `//host` is rejected so a crafted link cannot become an open redirect.
 */
export function safeNextPath(next: string | null, role: UserRole | null | undefined): string {
  if (next && next.startsWith('/') && !next.startsWith('//')) return next
  return postLoginPath(role)
}
