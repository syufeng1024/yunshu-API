import { act, cleanup, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '@/api/client'
import { playgroundChat } from '@/api/console'
import type {
  ConsoleModel,
  ConsoleOverview,
  KeyListItem,
  KeyListResponse,
  PlaygroundChatResult,
} from '@/api/console'
import { renderWithProviders } from '@/test/renderConsole'
import PlaygroundPage from './PlaygroundPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

vi.mock('@/api/console', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/console')>()
  return { ...actual, playgroundChat: vi.fn() }
})

const mockedGet = vi.mocked(api.get)
const mockedChat = vi.mocked(playgroundChat)

const overview: ConsoleOverview = {
  balance: {
    micro: 12_345_000,
    credits: '1234.5',
    cny: '1.23',
    recharge_micro: 10_000_000,
    bonus_micro: 2_345_000,
  },
  today: { requests: 4, consumed_micro: 500_000, consumed_credits: '0.05', success_rate_bp: 9850 },
  hourly: [],
  plan: { code: 'STANDARD', name: '标准版', policy_source: 'PLAN', trial_expire_at: null },
  quota: {
    rpm: 60,
    tpm: 100_000,
    concurrency: 5,
    daily_quota_micro: 50_000_000,
    used_today_micro: 10_000_000,
    remaining_micro: 40_000_000,
  },
  eta: {
    reachable: true,
    reason: 'OK',
    eta_at: '2026-10-27T10:00:00Z',
    seconds_left: 3600,
    burn_micro_per_hour: 1_000_000,
  },
}

const models: ConsoleModel[] = [
  {
    id: 11,
    model_name: 'text-large',
    display_name: 'Text Large',
    model_type: 'TEXT',
    context_length: 128_000,
    protocol: 'OPENAI_COMPAT',
    aliases: [],
  },
]

const modelsPage = {
  items: models,
  page: 1,
  page_size: 100,
  total: models.length,
}

const key: KeyListItem = {
  id: 31,
  key_name: '调试密钥',
  key_masked: 'sk-****abcd',
  status: 'ENABLED',
  expired: false,
  group_ratio_bp: null,
  model_scope_mode: 'ALL',
  model_count: 0,
  ip_whitelist_mode: 'ALL',
  ip_rule_count: 0,
  expire_at: null,
  today_consumed_micro: 0,
  created_at: '2026-10-01T00:00:00Z',
  last_used_at: null,
}

const keysPage: KeyListResponse = {
  items: [key],
  page: 1,
  page_size: 50,
  total: 1,
}

interface MockOptions {
  overview?: ConsoleOverview | ApiError
  models?: typeof modelsPage | ApiError
  keys?: KeyListResponse | ApiError
}

function installMock(options: MockOptions = {}) {
  mockedGet.mockImplementation((path: string) => {
    if (path === '/api/console/overview') {
      if (options.overview instanceof ApiError) return Promise.reject(options.overview)
      return Promise.resolve(options.overview ?? overview)
    }
    if (path.startsWith('/api/console/models?')) {
      if (options.models instanceof ApiError) return Promise.reject(options.models)
      return Promise.resolve(options.models ?? modelsPage)
    }
    if (path.startsWith('/api/console/keys?')) {
      if (options.keys instanceof ApiError) return Promise.reject(options.keys)
      return Promise.resolve(options.keys ?? keysPage)
    }
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

function terminalError(
  init: ConstructorParameters<typeof ApiError>[0],
  creditsRemaining: number | null = null,
): ApiError {
  const error = new ApiError(init) as ApiError & { creditsRemaining: number | null }
  error.creditsRemaining = creditsRemaining
  return error
}

async function waitForReady() {
  await screen.findByRole('option', { name: 'Text Large' })
  await waitFor(() => expect(screen.getByLabelText('模型')).toHaveValue('text-large'))
  await waitFor(() => expect(screen.getByLabelText('密钥')).toHaveValue('31'))
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedChat.mockReset()
})

afterEach(cleanup)

describe('PlaygroundPage', () => {
  it('F12-2: clicking a quick example fills the form without sending or billing', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)

    await waitForReady()
    await user.click(screen.getByRole('button', { name: '总结技术文档' }))

    expect(screen.getByLabelText('System 提示词')).toHaveValue(
      '你是一名技术编辑，回答使用简体中文，先给结论。',
    )
    expect(screen.getByLabelText('消息 1 内容')).toHaveValue(
      '请把下面的技术文档总结成三条要点：\n',
    )
    expect(screen.getByLabelText('temperature')).toHaveValue(0.3)
    expect(screen.getByLabelText('max_tokens')).toHaveValue(512)

    expect(mockedChat).not.toHaveBeenCalled()
    for (const call of mockedGet.mock.calls) {
      expect(String(call[0])).not.toContain('/v1/')
    }
  })

  it('sends key_id outside body and renders streamed deltas one by one', async () => {
    installMock()
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })

    mockedChat.mockImplementation(async ({ onDelta, onDone }) => {
      onDelta?.('你')
      await gate
      onDelta?.('好')
      onDone?.()
      return {
        creditsRemaining: 999.5,
        payload: null,
        content: '你好',
        streamed: true,
      }
    })

    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)
    await waitForReady()

    await user.click(screen.getByRole('button', { name: '发送' }))

    await waitFor(() =>
      expect(screen.getByTestId('playground-reply-content')).toHaveTextContent('你'),
    )
    expect(screen.getByText('▍')).toBeInTheDocument()

    await act(async () => {
      release()
    })

    await waitFor(() =>
      expect(screen.getByTestId('playground-reply-content')).toHaveTextContent('你好'),
    )
    expect(screen.queryByText('▍')).not.toBeInTheDocument()
    expect(
      screen.getByText('本次调用后余额：999.5 积分（X-Credits-Remaining）'),
    ).toBeInTheDocument()

    expect(mockedChat).toHaveBeenCalledTimes(1)
    const request = mockedChat.mock.calls[0][0]
    expect(request.keyId).toBe(31)
    expect(request.body).toEqual({
      model: 'text-large',
      messages: [],
      temperature: 0.7,
      max_tokens: 1024,
      stream: true,
    })
    expect(request.body).not.toHaveProperty('key_id')
    expect(request.signal).toBeInstanceOf(AbortSignal)
  })

  it('F12-3: a zero balance blocks the request and links to recharge', async () => {
    installMock({
      overview: {
        ...overview,
        balance: { ...overview.balance, micro: 0, credits: '0', cny: '0.00' },
      },
    })
    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)

    expect(await screen.findByText(/余额不足/)).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: '充值' })).toHaveAttribute(
      'href',
      '/console/recharge',
    )

    const send = screen.getByRole('button', { name: '发送' })
    expect(send).toBeDisabled()
    await user.click(send)
    expect(mockedChat).not.toHaveBeenCalled()
  })

  it('asks for a key when the account has none and never sends', async () => {
    installMock({ keys: { items: [], page: 1, page_size: 50, total: 0 } })
    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)

    expect(await screen.findByText(/请先在/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '密钥管理页' })).toHaveAttribute(
      'href',
      '/console/keys',
    )

    const send = screen.getByRole('button', { name: '发送' })
    expect(send).toBeDisabled()
    await user.click(send)
    expect(mockedChat).not.toHaveBeenCalled()
  })

  it('handles a backend insufficient_credits 402 with the header balance', async () => {
    installMock()
    mockedChat.mockRejectedValue(
      terminalError(
        {
          status: 402,
          type: 'billing_error',
          code: 'insufficient_credits',
          message: '余额不足，无法完成本次请求。',
          hint: '请充值后重试。',
        },
        -1.5,
      ),
    )

    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)
    await waitForReady()

    await user.click(screen.getByRole('button', { name: '发送' }))

    expect(await screen.findByText('余额不足，无法完成本次请求。')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '充值' })).toHaveAttribute(
      'href',
      '/console/recharge',
    )
    expect(
      await screen.findByText('本次调用后余额：-1.5 积分（X-Credits-Remaining）'),
    ).toBeInTheDocument()
  })

  it('shows the backend error code message and hint', async () => {
    installMock()
    mockedChat.mockRejectedValue(
      terminalError({
        status: 403,
        type: 'authorization_error',
        code: 'model_not_authorized',
        message: '该密钥无权调用此模型。',
        hint: '在密钥详情页调整模型范围。',
      }),
    )

    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)
    await waitForReady()

    await user.click(screen.getByRole('button', { name: '发送' }))

    expect(
      await screen.findByText('该密钥无权调用此模型。'),
    ).toBeInTheDocument()
    expect(screen.getByText('在密钥详情页调整模型范围。')).toBeInTheDocument()
  })

  it('stops the stream on demand, keeps partial text and marks the turn aborted', async () => {
    installMock()
    mockedChat.mockImplementation(
      ({ onDelta, signal }) =>
        new Promise<PlaygroundChatResult>((_resolve, reject) => {
          onDelta?.('半')
          const abort = () => reject(new DOMException('Aborted', 'AbortError'))
          if (signal?.aborted) {
            abort()
            return
          }
          signal?.addEventListener('abort', abort)
        }),
    )

    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)
    await waitForReady()

    await user.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() =>
      expect(screen.getByTestId('playground-reply-content')).toHaveTextContent('半'),
    )

    await user.click(screen.getByRole('button', { name: '停止' }))

    expect(await screen.findByText('已中止')).toBeInTheDocument()
    expect(screen.getByTestId('playground-reply-content')).toHaveTextContent('半')
    expect(screen.getByRole('button', { name: '发送' })).toBeInTheDocument()
  })

  it('says consumption is on the usage page when the header is missing', async () => {
    installMock()
    mockedChat.mockResolvedValue({
      creditsRemaining: null,
      payload: null,
      content: '完成',
      streamed: true,
    })

    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)
    await waitForReady()

    await user.click(screen.getByRole('button', { name: '发送' }))

    expect(await screen.findByText('本次消耗以用量页为准。')).toBeInTheDocument()
  })

  it('generates call code with the key mask only', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)
    await waitForReady()

    expect(screen.getByText(/Bearer sk-\*\*\*\*abcd/)).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('调用代码语言'), 'node')
    expect(screen.getByText(/Bearer sk-\*\*\*\*abcd/)).toBeInTheDocument()
    expect(screen.getByText(/const payload =/)).toBeInTheDocument()
  })

  it('shows -- and the backend message/hint when the balance cannot be loaded', async () => {
    installMock({
      overview: new ApiError({
        status: 0,
        type: 'network_error',
        code: 'network_error',
        message: '网络连接失败，请检查网络后重试。',
        hint: '确认网络可达后重新加载页面。',
      }),
    })
    renderWithProviders(<PlaygroundPage />)

    expect(
      await screen.findByText('网络连接失败，请检查网络后重试。'),
    ).toBeInTheDocument()
    expect(screen.getByText('确认网络可达后重新加载页面。')).toBeInTheDocument()
    expect(screen.getByText('--')).toBeInTheDocument()
    expect(screen.queryByText('0 积分')).not.toBeInTheDocument()
  })

  it('keeps the request preview local until send is pressed', async () => {
    installMock()
    renderWithProviders(<PlaygroundPage />)

    expect(
      await screen.findByRole('heading', { name: '请求预览' }),
    ).toBeInTheDocument()
    await waitFor(() => {
      // The model name appears in both the request preview and the generated
      // curl snippet, so assert presence rather than a unique match.
      expect(screen.getAllByText(/text-large/).length).toBeGreaterThan(0)
    })
    expect(mockedChat).not.toHaveBeenCalled()
  })
})
