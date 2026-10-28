import { cleanup, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { AdminUser, AdminUserDetailResponse } from '@/api/admin'
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

  it('UsersPage：余额调整显式不可用', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<AdminUsersPage />)
    await screen.findByText('演示账号')

    await user.click(screen.getByRole('button', { name: '详情' }))
    await screen.findByText('总余额')

    expect(screen.getByRole('button', { name: '调整余额' })).toBeDisabled()
    expect(
      screen.getByText('后端暂未提供余额调整接口（需 T24）'),
    ).toBeInTheDocument()
    expect(screen.getByText('2 个活跃 Key')).toBeInTheDocument()
  })
})
