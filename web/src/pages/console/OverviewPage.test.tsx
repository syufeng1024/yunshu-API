import { cleanup, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '@/api/client'
import type { ConsoleOverview } from '@/api/console'
import { renderWithProviders } from '@/test/renderConsole'
import OverviewPage from './OverviewPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)

const overview: ConsoleOverview = {
  balance: {
    micro: 12_345_000,
    credits: '1234.5',
    cny: '1.23',
    recharge_micro: 10_000_000,
    bonus_micro: 2_345_000,
  },
  today: {
    requests: 42,
    consumed_micro: 500_000,
    consumed_credits: '0.05',
    success_rate_bp: 9850,
  },
  hourly: [
    { hour: '2026-10-27T08:00:00+08:00', requests: 3, consumed_micro: 10_000 },
    { hour: '2026-10-27T09:00:00+08:00', requests: 5, consumed_micro: 20_000 },
  ],
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

function mockOverview(data: ConsoleOverview) {
  mockedGet.mockImplementation((path: string) => {
    if (path === '/api/console/overview') return Promise.resolve(data)
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

beforeEach(() => {
  mockedGet.mockReset()
})

afterEach(cleanup)

describe('OverviewPage', () => {
  it('renders balance from the backend cny string without a local conversion', async () => {
    mockOverview(overview)
    renderWithProviders(<OverviewPage />)

    expect(await screen.findByText('¥1.23')).toBeInTheDocument()
    expect(screen.getByText('1234.5 积分')).toBeInTheDocument()
    expect(screen.getByText('98.5%')).toBeInTheDocument()
  })

  it('F5-3: never renders a source-channel badge in V1.0', async () => {
    mockOverview(overview)
    renderWithProviders(<OverviewPage />)
    await screen.findByText('¥1.23')
    expect(screen.queryByText(/来源渠道/)).not.toBeInTheDocument()
  })

  it('shows 无日额度上限 when the plan has no daily ceiling', async () => {
    mockOverview({
      ...overview,
      quota: { ...overview.quota, daily_quota_micro: null, remaining_micro: null },
      eta: {
        reachable: false,
        reason: 'NO_DAILY_QUOTA',
        eta_at: null,
        seconds_left: null,
        burn_micro_per_hour: 0,
      },
    })
    renderWithProviders(<OverviewPage />)

    expect(await screen.findByText('无日额度上限')).toBeInTheDocument()
  })

  it('F5-2: a failed request renders an error state, not zero values', async () => {
    mockedGet.mockRejectedValue(
      new ApiError({
        status: 0,
        type: 'network_error',
        code: 'network_error',
        message: '网络连接失败，请检查网络后重试。',
        hint: '确认网络可达后重新加载页面。',
      }),
    )
    renderWithProviders(<OverviewPage />)

    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.getByText('网络连接失败，请检查网络后重试。')).toBeInTheDocument()
    expect(screen.queryByText('¥0.00')).not.toBeInTheDocument()
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
  })
})
