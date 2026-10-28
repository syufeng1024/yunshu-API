import { cleanup, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { AdminUser, AdminUserDetailResponse } from '@/api/admin'
import { ToastViewport } from '@/components'
import { useToastStore } from '@/stores/toast'
import { renderWithProviders } from '@/test/renderConsole'
import AdminUsersPage from './UsersPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)
const mockedPost = vi.mocked(api.post)

const demoUser: AdminUser = {
  id: 501,
  user_no: 'U202600501',
  email: 'ops-demo@example.com',
  phone: '13800138000',
  nickname: '演示账号',
  role: 'PERSONAL',
  status: 'NORMAL',
  risk_score: 12,
  plan_id: 2,
  plan_code: 'STANDARD',
  plan_name: '标准版',
  key_count: 2,
  created_at: '2026-03-01T02:00:00Z',
  updated_at: '2026-10-01T02:00:00Z',
}

const detail: AdminUserDetailResponse = {
  user: demoUser,
  balance: {
    exists: true,
    balance_recharge_micro: 10_000_000,
    balance_bonus_micro: 5_000_000,
    balance_micro: 15_000_000,
    frozen_refund_micro: 0,
    overdraft_limit_micro: 0,
    total_recharge_micro: 10_000_000,
    total_bonus_micro: 5_000_000,
    total_consumed_micro: 0,
    last_settled_at: null,
  },
  latest_balance_adjust: {
    id: 9001,
    direction: 'INCREASE',
    amount_micro: 50_000_000,
    reason: '上游故障补偿',
    state: 'PENDING',
    applicant_id: 9001,
    decided_by: null,
    decided_at: null,
    created_at: '2026-10-27T02:00:00Z',
  },
}

const emptyAudit = {
  items: [],
  page: 1,
  page_size: 50,
  total: 0,
  from: '2026-10-21T00:00:00Z',
  to: '2026-10-28T00:00:00Z',
}

function installMock() {
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/admin/users?')) {
      return Promise.resolve({ items: [demoUser], page: 1, page_size: 20, total: 1 })
    }
    if (path === '/api/admin/users/501') return Promise.resolve(detail)
    if (path.startsWith('/api/admin/audit-logs?')) return Promise.resolve(emptyAudit)
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedPost.mockReset()
  useToastStore.setState({ toasts: [] })
})

afterEach(cleanup)

describe('UsersPage', () => {
  it('UsersPage：搜索按字段发出对应参数', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<AdminUsersPage />)
    await screen.findByText('演示账号')

    await user.selectOptions(screen.getByLabelText('搜索字段'), 'phone')
    await user.type(screen.getByLabelText('搜索用户'), '13800138000')

    await waitFor(
      () => {
        const calls = mockedGet.mock.calls
          .map((call) => call[0])
          .filter((path) => path.startsWith('/api/admin/users?'))
        const last = calls[calls.length - 1] ?? ''
        const params = new URL(last, 'http://localhost').searchParams
        expect(params.get('phone')).toBe('13800138000')
      },
      { timeout: 3_000 },
    )

    const calls = mockedGet.mock.calls
      .map((call) => call[0])
      .filter((path) => path.startsWith('/api/admin/users?'))
    const params = new URL(calls[calls.length - 1] ?? '', 'http://localhost').searchParams
    expect(params.has('q')).toBe(false)
  })

  it('UsersPage：余额调整可提交并展示最近一张调整单', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<AdminUsersPage />)
    await screen.findByText('演示账号')

    await user.click(screen.getByRole('button', { name: '详情' }))
    await screen.findByText('总余额')

    expect(screen.getByRole('button', { name: '调整余额' })).toBeEnabled()
    expect(screen.queryByText(/后端暂未提供/)).not.toBeInTheDocument()
    expect(screen.getByText('2 个活跃 Key')).toBeInTheDocument()
    expect(screen.getByText('待审核')).toBeInTheDocument()
    expect(screen.getByText('上游故障补偿')).toBeInTheDocument()
  })

  it('UsersPage：余额调整提交后提示已提交审核', async () => {
    installMock()
    mockedPost.mockResolvedValue({
      balance_adjust: {
        id: 9002,
        user_id: 501,
        direction: 'INCREASE',
        amount_micro: 5_000_000,
        reason: '客服补偿积分',
        state: 'PENDING',
        applicant_id: 9001,
        created_at: '2026-10-28T02:00:00Z',
        updated_at: '2026-10-28T02:00:00Z',
      },
      review_task_id: 61,
    })
    const user = userEvent.setup()
    renderWithProviders(
      <>
        <AdminUsersPage />
        <ToastViewport />
      </>,
    )
    await screen.findByText('演示账号')

    await user.click(screen.getByRole('button', { name: '详情' }))
    await screen.findByText('总余额')
    await user.click(screen.getByRole('button', { name: '调整余额' }))
    await user.type(screen.getByLabelText(/金额（微积分/), '5000000')
    await user.type(screen.getByLabelText(/调整理由/), '客服补偿积分')
    await user.click(screen.getByRole('button', { name: '提交审核' }))

    expect(await screen.findByText('已提交审核')).toBeInTheDocument()
    const [path, body] = mockedPost.mock.calls.at(-1) ?? []
    expect(path).toBe('/api/admin/users/501/balance-adjust')
    expect(body).toMatchObject({
      direction: 'INCREASE',
      amount_micro: 5_000_000,
      reason: '客服补偿积分',
    })
  })
})
