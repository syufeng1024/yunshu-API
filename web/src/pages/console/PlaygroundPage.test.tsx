import { cleanup, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '@/api/client'
import type { ConsoleModel, ConsoleOverview } from '@/api/console'
import { renderWithProviders } from '@/test/renderConsole'
import PlaygroundPage from './PlaygroundPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)
const mockedPost = vi.mocked(api.post)

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

interface MockOptions {
  overview?: ConsoleOverview | ApiError
  models?: { items: ConsoleModel[]; page: number; page_size: number; total: number } | ApiError
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
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedPost.mockReset()
})

afterEach(cleanup)

describe('PlaygroundPage', () => {
  it('F12-2: clicking a quick example fills the form without sending or billing', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<PlaygroundPage />)

    await screen.findByRole('option', { name: 'Text Large' })
    await user.click(screen.getByRole('button', { name: '总结技术文档' }))

    expect(screen.getByLabelText('System 提示词')).toHaveValue(
      '你是一名技术编辑，回答使用简体中文，先给结论。',
    )
    expect(screen.getByLabelText('消息 1 内容')).toHaveValue(
      '请把下面的技术文档总结成三条要点：\n',
    )
    expect(screen.getByLabelText('temperature')).toHaveValue(0.3)
    expect(screen.getByLabelText('max_tokens')).toHaveValue(512)

    expect(mockedPost).not.toHaveBeenCalled()
    for (const call of mockedGet.mock.calls) {
      expect(String(call[0])).not.toContain('/v1/')
    }
  })

  it('F12-3 (degraded): a zero balance shows the recharge prompt and still sends nothing', async () => {
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

    const send = screen.getByRole('button', { name: /发送/ })
    expect(send).toBeDisabled()
    await user.click(send)
    expect(mockedPost).not.toHaveBeenCalled()
  })

  it('labels the missing proxy endpoint and points users at /docs', async () => {
    installMock()
    renderWithProviders(<PlaygroundPage />)

    expect(
      await screen.findByText(/后端新增控制台代理端点/),
    ).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '/docs' })).toHaveAttribute(
      'href',
      '/docs',
    )

    const send = screen.getByRole('button', { name: /发送/ })
    expect(send).toBeDisabled()
    expect(
      screen.getByText(/点击不会发送请求，也不会扣费/),
    ).toBeInTheDocument()
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

  it('keeps the request preview local and marks it as never sent', async () => {
    installMock()
    renderWithProviders(<PlaygroundPage />)

    expect(await screen.findByText('请求预览（不会发送）')).toBeInTheDocument()
    expect(screen.getByText(/页面不会发送/)).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByText(/text-large/)).toBeInTheDocument()
    })
    expect(mockedPost).not.toHaveBeenCalled()
  })
})
