import { execFileSync } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { expect } from '@playwright/test'
import type { Browser, BrowserContext, Page } from '@playwright/test'

/**
 * Shared setup for the three F4-2 / F7-1 / F19-1 specs. Accounts and keys are
 * created through the real control plane; F7-1 and F19-1 must not be mocked.
 */

export const E2E_BASE_URL = process.env.E2E_BASE_URL ?? 'http://127.0.0.1:5180'
const PASSWORD = 'E2ePassw0rd!'

export interface E2EUser {
  context: BrowserContext
  page: Page
  email: string
  userId: string
  csrf: string
}

export interface CreatedKey {
  id: number
  key: string
  key_masked: string
}

function uniqueEmail(prefix: string): string {
  const nonce = `${Date.now()}-${randomBytes(6).toString('hex')}`
  return `e2e-${prefix}-${nonce}@example.test`
}

/**
 * Registers a fresh PERSONAL account in a brand-new browser context. The
 * register response sets both the sid and csrf cookies and returns csrf_token.
 */
export async function registerUser(browser: Browser, prefix: string): Promise<E2EUser> {
  const context = await browser.newContext({ baseURL: E2E_BASE_URL })
  const email = uniqueEmail(prefix)
  const response = await context.request.post('/api/v1/auth/register', {
    data: { identifier: email, code: '', password: PASSWORD },
  })
  if (response.status() !== 201) {
    throw new Error(`register ${email}: ${response.status()} ${await response.text()}`)
  }
  const body = (await response.json()) as {
    user: { id: string }
    csrf_token?: string
  }
  const csrf =
    body.csrf_token ??
    (await context.cookies()).find((cookie) => cookie.name === 'csrf')?.value ??
    ''
  expect(csrf, 'register must issue a CSRF token').not.toBe('')
  const page = await context.newPage()
  return {
    context,
    page,
    email,
    userId: String(body.user.id),
    csrf,
  }
}

/** Creates one ALL-scope key and returns the one-time plaintext. */
export async function createKey(
  context: BrowserContext,
  csrf: string,
  name: string,
): Promise<CreatedKey> {
  const response = await context.request.post('/api/console/keys', {
    data: {
      name,
      count: 1,
      model_scope_mode: 'ALL',
      expire_at: '2030-01-01T00:00:00Z',
      ip_allowlist: [],
    },
    headers: { 'X-CSRF-Token': csrf },
  })
  if (!response.ok()) {
    throw new Error(`create key: ${response.status()} ${await response.text()}`)
  }
  const body = (await response.json()) as {
    items: Array<{ id: number; key: string; key_masked: string }>
  }
  expect(body.items).toHaveLength(1)
  return body.items[0]
}

/** A 36-char request id, shaped like the gateway's UUID/ULID values. */
export function requestId(prefix: string): string {
  const suffix = randomBytes(18).toString('hex')
  return (prefix + suffix).slice(0, 36)
}

function runSQL(sql: string): void {
  const db = process.env.E2E_DB_NAME ?? 'yunshu_e2e'
  const user = process.env.E2E_MYSQL_USER ?? 'root'
  if (process.env.E2E_MYSQL_HOST) {
    const host = process.env.E2E_MYSQL_HOST
    const port = process.env.E2E_MYSQL_PORT ?? '3306'
    const password = process.env.E2E_MYSQL_PASSWORD ?? 'test'
    execFileSync(
      'mysql',
      ['-h', host, '-P', port, `-u${user}`, `-p${password}`, db, '-e', sql],
      { stdio: 'pipe' },
    )
    return
  }
  const container = process.env.E2E_MYSQL_CONTAINER ?? 'yunshu-mysql'
  const password = process.env.E2E_MYSQL_PASSWORD ?? 'root'
  const docker = process.env.E2E_DOCKER_BIN ?? 'docker'
  execFileSync(
    docker,
    ['exec', container, 'mysql', `-u${user}`, `-p${password}`, db, '-e', sql],
    { stdio: 'pipe' },
  )
}

/**
 * Seeds one request-log row for a user. The request picker is a real SQL query
 * over t_usage_record; there is no API that creates usage, so the e2e fixture
 * writes the same row the gateway would.
 */
export function seedUsage(userId: string, requestID: string): void {
  if (!/^\d+$/.test(userId)) throw new Error(`unsafe userId: ${userId}`)
  if (!/^[A-Za-z0-9-]{1,36}$/.test(requestID)) {
    throw new Error(`unsafe request id: ${requestID}`)
  }
  const sql = `INSERT INTO t_usage_record
    (request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
     request_model_req, inbound_protocol, http_status, result, total_ms, created_at)
    VALUES ('${requestID}', ${userId}, 0, 'e2e', 'e2e-model', 'e2e-model',
            'OPENAI_COMPAT', 200, 'SUCCESS', 10, UTC_TIMESTAMP(3))`
  runSQL(sql)
}
