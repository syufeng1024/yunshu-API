import { describe, expect, it } from 'vitest'
import { postLoginPath, safeNextPath } from './authRoutes'

describe('post-login routing', () => {
  it('sends personal accounts to the console', () => {
    expect(postLoginPath('PERSONAL')).toBe('/console')
  })

  it('sends every admin role to /admin', () => {
    expect(postLoginPath('OPS')).toBe('/admin')
    expect(postLoginPath('FINANCE')).toBe('/admin')
    expect(postLoginPath('SUPER_ADMIN')).toBe('/admin')
  })

  it('keeps a same-origin next path and rejects protocol-relative redirects', () => {
    expect(safeNextPath('/console/keys', 'PERSONAL')).toBe('/console/keys')
    expect(safeNextPath('//evil.example/path', 'PERSONAL')).toBe('/console')
    expect(safeNextPath('https://evil.example', 'PERSONAL')).toBe('/console')
    expect(safeNextPath(null, 'OPS')).toBe('/admin')
  })
})
