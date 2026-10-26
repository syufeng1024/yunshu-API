import { ROUTES } from '@/lib/constants'
import type { components } from './types'

type ErrorEnvelope = components['schemas']['ErrorEnvelope']
type ErrorBody = components['schemas']['ErrorBody']

const API_BASE_URL = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/+$/, '')

export interface ApiRequestOptions extends Omit<RequestInit, 'body'> {
  body?: unknown
  /** Guard bootstrap opts out so it can map 401 to the anonymous state. */
  redirectOn401?: boolean
}

export interface ApiErrorInit {
  status: number
  code: string
  type: string
  message: string
  hint: string
  requestId?: string
  retryAfterSeconds?: number
}

/** Normalised failure from the control plane. Never carries a raw traceback. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly type: string
  readonly hint: string
  readonly requestId: string
  readonly retryAfterSeconds: number

  constructor(init: ApiErrorInit) {
    super(init.message)
    this.name = 'ApiError'
    this.status = init.status
    this.code = init.code
    this.type = init.type
    this.hint = init.hint
    this.requestId = init.requestId ?? ''
    this.retryAfterSeconds = init.retryAfterSeconds ?? 0
  }
}

function redirectToLogin(): void {
  if (typeof window === 'undefined') return
  const { pathname, search } = window.location
  if (pathname === ROUTES.login) return
  const next = encodeURIComponent(`${pathname}${search}`)
  window.location.assign(`${ROUTES.login}?next=${next}`)
}

function isErrorBody(value: unknown): value is ErrorBody {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Partial<ErrorBody>
  return typeof candidate.code === 'string' && typeof candidate.message === 'string'
}

function readEnvelope(payload: unknown): ErrorBody | null {
  if (typeof payload !== 'object' || payload === null) return null
  const envelope = payload as Partial<ErrorEnvelope>
  return isErrorBody(envelope.error) ? envelope.error : null
}

function fallbackMessage(status: number): string {
  if (status >= 500) return '服务暂时不可用，请稍后重试。'
  if (status === 404) return '请求的资源不存在。'
  return `请求失败（HTTP ${status}）。`
}

function serializeBody(body: unknown): BodyInit | undefined {
  if (body === undefined) return undefined
  if (
    typeof body === 'string' ||
    body instanceof FormData ||
    body instanceof Blob ||
    body instanceof URLSearchParams
  ) {
    return body
  }
  return JSON.stringify(body)
}

/**
 * Single fetch wrapper. Every non-2xx response is normalised into ApiError so
 * callers can render message + hint instead of an empty table or a zero value.
 */
async function request<T>(path: string, options: ApiRequestOptions = {}): Promise<T> {
  const { body, redirectOn401 = true, headers: rawHeaders, ...rest } = options
  const headers = new Headers(rawHeaders)
  headers.set('Accept', 'application/json')
  const serialized = serializeBody(body)
  if (serialized !== undefined && !(serialized instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  let response: Response
  try {
    response = await fetch(`${API_BASE_URL}${path}`, {
      ...rest,
      headers,
      body: serialized,
      credentials: 'include',
    })
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    throw new ApiError({
      status: 0,
      type: 'network_error',
      code: 'network_error',
      message: '网络连接失败，请检查网络后重试。',
      hint: '确认网络可达后重新加载页面。',
    })
  }

  if (response.status === 204) {
    return undefined as T
  }

  const text = await response.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text) as unknown
    } catch {
      payload = null
    }
  }

  if (!response.ok) {
    const detail = readEnvelope(payload)
    const apiError = new ApiError({
      status: response.status,
      type: detail?.type ?? 'internal_error',
      code: detail?.code ?? `http_${response.status}`,
      message: detail?.message ?? fallbackMessage(response.status),
      hint: detail?.hint ?? '',
      requestId: detail?.request_id,
      retryAfterSeconds: detail?.retry_after_seconds,
    })
    if (response.status === 401 && redirectOn401) {
      redirectToLogin()
    }
    throw apiError
  }

  return payload as T
}

export const api = {
  get<T>(path: string, options?: Omit<ApiRequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'GET' })
  },
  post<T>(path: string, body?: unknown, options?: Omit<ApiRequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'POST', body })
  },
  put<T>(path: string, body?: unknown, options?: Omit<ApiRequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'PUT', body })
  },
  patch<T>(path: string, body?: unknown, options?: Omit<ApiRequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'PATCH', body })
  },
  delete<T>(path: string, options?: Omit<ApiRequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'DELETE' })
  },
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message
  return '请求失败，请稍后重试。'
}

export function errorHint(error: unknown): string | null {
  if (error instanceof ApiError && error.hint) return error.hint
  return null
}
