import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { playgroundChat, playgroundCreditsRemaining } from './console'
import { CSRF_COOKIE, clearCsrfToken } from './csrf'

interface HeaderBag {
  get(name: string): string | null
}

function headersOf(values: Record<string, string>): HeaderBag {
  const normalized = new Map(
    Object.entries(values).map(([name, value]) => [name.toLowerCase(), value]),
  )
  return { get: (name) => normalized.get(name.toLowerCase()) ?? null }
}

function jsonResponse(
  payload: unknown,
  status = 200,
  headers: Record<string, string> = { 'Content-Type': 'application/json' },
): Response {
  const body = JSON.stringify(payload)
  return {
    status,
    ok: status >= 200 && status < 300,
    headers: headersOf(headers),
    text: async () => body,
  } as unknown as Response
}

function streamResponse(
  chunks: Uint8Array[],
  headers: Record<string, string> = {},
): Response {
  let index = 0
  return {
    status: 200,
    ok: true,
    headers: headersOf({
      'Content-Type': 'text/event-stream; charset=utf-8',
      'Cache-Control': 'no-cache',
      'X-Accel-Buffering': 'no',
      ...headers,
    }),
    body: {
      getReader: () => ({
        read: async () =>
          index < chunks.length
            ? { done: false, value: chunks[index++] }
            : { done: true, value: undefined },
        cancel: async () => {},
        releaseLock: () => {},
      }),
    },
    text: async () => '',
  } as unknown as Response
}

const encoder = new TextEncoder()

const body = {
  model: 'text-large',
  messages: [{ role: 'user', content: '你好' }],
  temperature: 0.7,
  max_tokens: 1024,
  stream: true,
}

describe('playgroundChat', () => {
  const fetchMock = vi.fn()

  beforeEach(() => {
    document.cookie = `${CSRF_COOKIE}=; path=/; max-age=0`
    clearCsrfToken()
    fetchMock.mockReset()
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    document.cookie = `${CSRF_COOKIE}=; path=/; max-age=0`
    clearCsrfToken()
    vi.unstubAllGlobals()
  })

  it('streams deltas in order and reads X-Credits-Remaining', async () => {
    document.cookie = `${CSRF_COOKIE}=csrf-from-cookie; path=/`
    fetchMock.mockResolvedValue(
      streamResponse(
        [
          encoder.encode('data: {"choices":[{"delta":{"content":"你"}}]}\n\n'),
          encoder.encode(
            'data: {"choices":[{"delta":{"content":"好"}}]}\n\ndata: [DONE]\n\n',
          ),
        ],
        { 'X-Credits-Remaining': '123.5' },
      ),
    )

    const deltas: string[] = []
    const onDone = vi.fn()
    const result = await playgroundChat({
      keyId: 7,
      body,
      onDelta: (text) => deltas.push(text),
      onDone,
    })

    expect(deltas).toEqual(['你', '好'])
    expect(onDone).toHaveBeenCalledTimes(1)
    expect(result).toMatchObject({
      creditsRemaining: 123.5,
      content: '你好',
      streamed: true,
    })

    const init = fetchMock.mock.calls[0][1] as RequestInit
    const headers = init.headers as Headers
    expect(headers.get('X-CSRF-Token')).toBe('csrf-from-cookie')
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(init.credentials).toBe('include')
    expect(init.method).toBe('POST')

    // key_id belongs to the outer envelope; the forwarded body is untouched.
    const sent = JSON.parse(String(init.body)) as {
      key_id: number
      body: Record<string, unknown>
    }
    expect(sent.key_id).toBe(7)
    expect(sent.body).toEqual(body)
    expect(sent.body).not.toHaveProperty('key_id')
  })

  it('throws ApiError with X-Credits-Remaining on 402', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        {
          error: {
            type: 'billing_error',
            code: 'insufficient_credits',
            message: '余额不足，无法完成本次请求。',
            hint: '请充值后重试。',
            request_id: 'req-402',
            retry_after_seconds: 0,
          },
        },
        402,
        { 'Content-Type': 'application/json', 'X-Credits-Remaining': '-12.34' },
      ),
    )

    const error = await playgroundChat({ keyId: 2, body }).catch((reason: unknown) => reason)

    expect(error).toMatchObject({
      status: 402,
      code: 'insufficient_credits',
      message: '余额不足，无法完成本次请求。',
      hint: '请充值后重试。',
    })
    expect(playgroundCreditsRemaining(error)).toBe(-12.34)
  })

  it('renders a non-streaming JSON response in one delta', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        { choices: [{ message: { role: 'assistant', content: '一次性输出' } }] },
        200,
        { 'Content-Type': 'application/json', 'X-Credits-Remaining': '88' },
      ),
    )

    const onDelta = vi.fn()
    const onDone = vi.fn()
    const result = await playgroundChat({ keyId: 3, body: { ...body, stream: false }, onDelta, onDone })

    expect(onDelta).toHaveBeenCalledTimes(1)
    expect(onDelta).toHaveBeenCalledWith('一次性输出')
    expect(onDone).toHaveBeenCalledTimes(1)
    expect(result).toMatchObject({
      creditsRemaining: 88,
      content: '一次性输出',
      streamed: false,
    })
    expect(result.payload).toEqual({
      choices: [{ message: { role: 'assistant', content: '一次性输出' } }],
    })
  })

  it('stops reading when the AbortSignal fires and keeps delivered deltas', async () => {
    const controller = new AbortController()
    let reads = 0

    fetchMock.mockImplementation(() =>
      Promise.resolve({
        status: 200,
        ok: true,
        headers: headersOf({ 'Content-Type': 'text/event-stream; charset=utf-8' }),
        body: {
          getReader: () => ({
            read: () => {
              reads += 1
              if (reads === 1) {
                return Promise.resolve({
                  done: false,
                  value: encoder.encode(
                    'data: {"choices":[{"delta":{"content":"半"}}]}\n\n',
                  ),
                })
              }
              return new Promise((_resolve, reject) => {
                const abort = () => reject(new DOMException('Aborted', 'AbortError'))
                if (controller.signal.aborted) {
                  abort()
                  return
                }
                controller.signal.addEventListener('abort', abort)
              })
            },
            cancel: async () => {},
            releaseLock: () => {},
          }),
        },
        text: async () => '',
      } as unknown as Response),
    )

    const deltas: string[] = []
    const onDone = vi.fn()
    const promise = playgroundChat({
      keyId: 4,
      body,
      signal: controller.signal,
      onDelta: (text) => deltas.push(text),
      onDone,
    })

    await vi.waitFor(() => expect(deltas).toEqual(['半']))
    controller.abort()

    await expect(promise).rejects.toMatchObject({ name: 'AbortError' })
    expect(deltas).toEqual(['半'])
    expect(onDone).not.toHaveBeenCalled()
    expect((fetchMock.mock.calls[0][1] as RequestInit).signal).toBe(controller.signal)
  })

  it('surfaces a mid-stream error frame as ApiError', async () => {
    fetchMock.mockResolvedValue(
      streamResponse([
        encoder.encode(
          'data: {"error":{"type":"authorization_error","code":"model_not_authorized","message":"该密钥无权调用此模型。","hint":"在密钥详情页调整模型范围。","request_id":"req-1","retry_after_seconds":0}}\n\n',
        ),
      ]),
    )

    const error = await playgroundChat({ keyId: 5, body }).catch((reason: unknown) => reason)

    expect(error).toMatchObject({
      status: 200,
      code: 'model_not_authorized',
      message: '该密钥无权调用此模型。',
    })
  })
})
