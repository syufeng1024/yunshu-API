import { cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { AdminPriceRule, PriceGroup } from '@/api/admin'
import { earliestEffectiveAt, toLocalDateTimeValue } from '@/components/admin'
import { renderWithProviders } from '@/test/renderConsole'
import AdminPricingPage from './PricingPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)
const mockedPatch = vi.mocked(api.patch)

function priceRule(overrides: Partial<AdminPriceRule> = {}): AdminPriceRule {
  return {
    id: 701,
    scope_level: 'MODEL',
    model_id: 11,
    model_name: 'qwen-max',
    model_type: 'TEXT',
    in_price_micro_per_k: 1000,
    out_price_micro_per_k: 3000,
    per_call_price_micro: 0,
    cost_in_micro_per_k: 800,
    cost_out_micro_per_k: 2400,
    currency_cost: 'CNY',
    version: 3,
    effective_from: '2026-10-01T00:00:00Z',
    effective_to: null,
    enabled: true,
    created_at: '2026-09-20T02:00:00Z',
    ...overrides,
  }
}

const active = priceRule({ effective_to: '2026-11-05T00:00:00Z' })

const groups: PriceGroup[] = [
  {
    id: 21,
    code: 'internal',
    name: '内部调用',
    ratio_bp: 0,
    model_scope_mode: 'ALL',
    channel_scope_mode: 'ALL',
    self_service: false,
    remark: '不计费',
    enabled: true,
    model_ids: [],
    channel_ids: [],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 22,
    code: 'standard',
    name: '标准分组',
    ratio_bp: 8500,
    model_scope_mode: 'ALL',
    channel_scope_mode: 'ALL',
    self_service: true,
    remark: '',
    enabled: true,
    model_ids: [],
    channel_ids: [],
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
]

const models = [
  {
    id: 11,
    model_name: 'qwen-max',
    display_name: 'Qwen Max',
    region: 'DOMESTIC' as const,
    model_type: 'TEXT' as const,
    protocol_id: 1,
    protocol: 'BAILIAN',
    context_length: 32768,
    capability_mask: 0,
    capabilities: { stream: null, tool_call: null, vision: null, json_mode: null },
    listing_state: 'LISTED' as const,
    recommend_weight: 100,
    aliases: [],
    pricing: null,
    created_at: '2026-07-01T02:00:00Z',
    updated_at: '2026-10-01T08:00:00Z',
  },
]

const emptyAudit = {
  items: [],
  page: 1,
  page_size: 50,
  total: 0,
  from: '2026-10-21T00:00:00Z',
  to: '2026-10-28T00:00:00Z',
}

function installMock(rules: AdminPriceRule[] = [active]) {
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/admin/prices?')) {
      if (path.includes('model_id=11')) {
        return Promise.resolve({
          items: rules,
          page: 1,
          page_size: 100,
          total: rules.length,
        })
      }
      return Promise.resolve({
        items: [active],
        page: 1,
        page_size: 20,
        total: 1,
      })
    }
    if (path === '/api/admin/groups') return Promise.resolve({ items: groups, total: groups.length })
    if (path.startsWith('/api/admin/models?')) {
      return Promise.resolve({ items: models, page: 1, page_size: 100, total: models.length })
    }
    if (path.startsWith('/api/admin/audit-logs?')) return Promise.resolve(emptyAudit)
    return Promise.reject(new Error(`unexpected ${path}`))
  })
  mockedPatch.mockResolvedValue({ price: active })
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedPatch.mockReset()
})

afterEach(cleanup)

async function openPriceEditor() {
  const user = userEvent.setup()
  renderWithProviders(<AdminPricingPage />)
  await screen.findByText('qwen-max')
  await user.click(screen.getByRole('button', { name: '编辑' }))
  await screen.findByLabelText(/输入单价/)
  return user
}

describe('PricingPage', () => {
  it('PricingPage：价格填 0 或负数在字段上提示', async () => {
    installMock()
    const user = await openPriceEditor()

    const input = screen.getByLabelText(/输入单价/)
    await user.clear(input)
    await user.type(input, '0')
    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(await screen.findByText('输入单价必须大于 0。')).toBeInTheDocument()
    expect(mockedPatch).not.toHaveBeenCalled()

    await user.clear(input)
    await user.type(input, '-1')
    await user.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText('输入单价必须为非负整数。')).toBeInTheDocument()
    expect(mockedPatch).not.toHaveBeenCalled()
  })

  it('PricingPage：涨价时生效时间最小 today+7', async () => {
    installMock()
    const user = await openPriceEditor()
    const input = screen.getByLabelText(/输入单价/)
    const raiseDate = toLocalDateTimeValue(earliestEffectiveAt('raise')).slice(0, 10)
    const today = toLocalDateTimeValue(earliestEffectiveAt('cut')).slice(0, 10)

    await user.clear(input)
    await user.type(input, '1200')
    await waitFor(() => {
      const time = screen.getByTestId('effective-time-input') as HTMLInputElement
      expect(time.min.startsWith(raiseDate)).toBe(true)
    })

    await user.clear(input)
    await user.type(input, '900')
    await waitFor(() => {
      const time = screen.getByTestId('effective-time-input') as HTMLInputElement
      expect(time.min.startsWith(today)).toBe(true)
    })
  })

  it('PricingPage：地板不满足显示差额', async () => {
    installMock()
    await openPriceEditor()

    const difference = await screen.findByTestId('floor-check-difference')
    expect(difference).toHaveTextContent('差额 217 积分/千 token')
  })

  it('PricingPage：两处配价冲突指出两份来源', async () => {
    installMock([
      priceRule({ id: 701 }),
      priceRule({ id: 702, in_price_micro_per_k: 1200, version: 4 }),
    ])
    await openPriceEditor()

    const banner = await screen.findByTestId('price-conflict-banner')
    expect(within(banner).getByText('t_price_rule#701')).toBeInTheDocument()
    expect(within(banner).getByText('t_price_rule#702')).toBeInTheDocument()
    expect(within(banner).getAllByRole('button', { name: '跳到该行' })).toHaveLength(2)
  })

  it('PricingPage：internal 分组显示不计费', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<AdminPricingPage />)
    await screen.findByText('qwen-max')

    await user.click(screen.getByRole('tab', { name: '分组倍率' }))

    expect(await screen.findByText('不计费')).toBeInTheDocument()
    expect(screen.queryByText('免费')).not.toBeInTheDocument()
    expect(screen.queryByText('0 折')).not.toBeInTheDocument()
    expect(screen.queryByText('100 折')).not.toBeInTheDocument()
  })
})
