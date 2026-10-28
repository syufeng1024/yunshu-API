import { cleanup, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { AdminOverview } from '@/api/admin'
import { renderWithProviders } from '@/test/renderConsole'
import AdminOverviewPage from './OverviewPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)

function overview(successRate: number | null): AdminOverview {
  return {
    today: { requests: 12, consumed_micro: 34_000_000, success_rate_bp: successRate },
    alerts: { dangling_prehold: 11, recon_diff: 22, sla_overdue: 33, channel_abnormal: 44 },
    counts: { pending_reviews: 5, open_tickets: 6 },
  }
}

beforeEach(() => {
  mockedGet.mockReset()
})

afterEach(cleanup)

describe('AdminOverviewPage', () => {
  it('AdminOverviewPage：成功率为 null 时渲染 —，不渲染 0%', async () => {
    mockedGet.mockResolvedValue(overview(null))
    renderWithProviders(<AdminOverviewPage />)

    expect(await screen.findByText('12')).toBeInTheDocument()
    expect(screen.getByText('3,400 积分')).toBeInTheDocument()
    expect(screen.getByText('—')).toBeInTheDocument()
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
    expect(screen.getByText('悬挂预扣')).toBeInTheDocument()
    expect(screen.getByText('11')).toBeInTheDocument()
    expect(screen.getByText('22')).toBeInTheDocument()
    expect(screen.getByText('33')).toBeInTheDocument()
    expect(mockedGet).toHaveBeenCalledWith('/api/admin/overview')
  })

  it('AdminOverviewPage：有分母时显示真实成功率', async () => {
    mockedGet.mockResolvedValue(overview(9850))
    renderWithProviders(<AdminOverviewPage />)

    expect(await screen.findByText('98.5%')).toBeInTheDocument()
    expect(screen.queryByText('—')).not.toBeInTheDocument()
  })
})
