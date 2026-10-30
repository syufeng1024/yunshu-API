/**
 * Single source of truth for the gateway origin the browser should talk to.
 *
 * Production MUST inject `VITE_API_BASE_URL` at build time (OI-12 / G9). When it
 * is absent — the containerised prototype serves the SPA from the same origin
 * as the gateway — we fall back to the page's own origin instead of hardcoding
 * a domain: a baked-in hostname would silently send traffic to someone else's
 * server the moment the stack is deployed elsewhere.
 */
export function resolveApiBaseUrl(): string {
  const configured = (import.meta.env.VITE_API_BASE_URL ?? '').trim().replace(/\/+$/, '')
  if (configured) return configured
  if (typeof window === 'undefined') return ''
  return window.location.origin.replace(/\/+$/, '')
}

/**
 * Gateway prefix that callers must hit, e.g. `https://api.example.com/v1`.
 * Empty string when neither the env var nor `window` is available (SSR/build
 * pre-render), so no stray `/v1` is ever rendered.
 */
export function resolveGatewayBaseUrl(): string {
  const base = resolveApiBaseUrl()
  return base ? `${base}/v1` : ''
}
