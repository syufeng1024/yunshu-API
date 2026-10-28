import { cleanup, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { AdminModel } from '@/api/admin'
import { ToastViewport } from '@/components'
import { useToastStore } from '@/stores/toast'
import { renderWithProviders } from '@/test/renderConsole'
import AdminModelsPage from './ModelsPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)
const mockedPost = vi.mocked(api.post)

const pricing: AdminModel['pricing'] = {
  price_rule_id: 701,
  version: 3,
  effective_from: '2026-10-01T00:00:00Z',
  in_price_micro_per_k: 1000,
  out_price_micro_per_k: 3000,
  per_call_price_micro: 0,
}

function model(overrides: Partial<AdminModel> = {}): AdminModel {
  return {
    id: 12,
    model_name: 'deepseek-chat',
    display_name: 'DeepSeek Chat',
    region: 'DOMESTIC',
    model_type: 'TEXT',
    protocol_id: 2,
    protocol: 'OPENAI_COMPAT',
    primary_channel_id: 2,
    context_length: 65536,
    capability_mask: 1,
    capabilities: { stream: true, tool_call: false, vision: false, json_mode: null },
    listing_state: 'CATALOG_ONLY',
    recommend_weight: 0,
    published_at: null,
    delisted_at: null,
    aliases: [],
    pricing: null,
    created_at: '2026-08-20T02:00:00Z',
    updated_at: '2026-10-18T02:00:00Z',
    ...overrides,
  }
}

const unpricedModel = model()

const catalogPriced = model({
  id: 14,
  model_name: 'catalog-priced',
  display_name: '待上架模型',
  listing_state: 'CATALOG_ONLY',
  pricing,
})

const delistedPriced = model({
  id: 15,
  model_name: 'delisted-priced',
  display_name: '已下架模型',
  listing_state: 'DELISTED',
  pricing,
})

const listedModel = model({
  id: 16,
  model_name: 'listed-model',
  display_name: '已上架模型',
  listing_state: 'LISTED',
  recommend_weight: 100,
  pricing,
})

function installGet(items: AdminModel[]) {
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/admin/models?')) {
      return Promise.resolve({
        items,
        page: 1,
        page_size: 20,
        total: items.length,
      })
    }
    if (path.startsWith('/api/admin/audit-logs?')) {
      return Promise.resolve({
        items: [],
        page: 1,
        page_size: 50,
        total: 0,
        from: '2026-10-21T00:00:00Z',
        to: '2026-10-28T00:00:00Z',
      })
    }
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedPost.mockReset()
  useToastStore.setState({ toasts: [] })
  installGet([unpricedModel])
  mockedPost.mockResolvedValue({ model: listedModel })
})

afterEach(cleanup)

describe('ModelsPage', () => {
  it('ModelsPage：未配价上架给出前置提示', async () => {
    renderWithProviders(<AdminModelsPage />)

    await screen.findByText('DeepSeek Chat')
    expect(screen.getByTestId('pricing-required-hint')).toHaveTextContent(
      '先配置价格才能上架',
    )
    expect(screen.getByRole('button', { name: '上架' })).toBeDisabled()
  })

  it('ModelsPage：上下架成功提示公告草稿，DELISTED 不谎报', async () => {
    installGet([catalogPriced, delistedPriced])
    const user = userEvent.setup()
    renderWithProviders(
      <>
        <AdminModelsPage />
        <ToastViewport />
      </>,
    )
    await screen.findByText('待上架模型')

    const firstTable = screen.getByRole('table', { name: '模型主数据列表' })
    const catalogRow = within(firstTable).getByText('待上架模型').closest('tr')
    await user.click(within(catalogRow as HTMLElement).getByRole('button', { name: '上架' }))
    expect(await screen.findByText('已生成待发布公告（草稿）')).toBeInTheDocument()

    const secondTable = screen.getByRole('table', { name: '模型主数据列表' })
    const delistedRow = within(secondTable).getByText('已下架模型').closest('tr')
    await user.click(within(delistedRow as HTMLElement).getByRole('button', { name: '上架' }))

    expect(await screen.findByText('上架状态已更新')).toBeInTheDocument()
    expect(screen.getByText('该状态转换不生成公告草稿。')).toBeInTheDocument()
    expect(screen.queryAllByText('已生成待发布公告（草稿）').length).toBeLessThanOrEqual(1)
  })

  it('ModelsPage：推荐权重负数或超上限在字段上拦截', async () => {
    installGet([listedModel])
    const user = userEvent.setup()
    renderWithProviders(<AdminModelsPage />)
    await screen.findByText('已上架模型')

    const table = screen.getByRole('table', { name: '模型主数据列表' })
    const row = within(table).getByText('已上架模型').closest('tr')
    await user.click(within(row as HTMLElement).getByRole('button', { name: '推荐权重' }))

    const input = await screen.findByLabelText('推荐权重')
    await user.clear(input)
    await user.type(input, '-1')
    await user.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText('推荐权重不能为负数。')).toBeInTheDocument()
    expect(mockedPost).not.toHaveBeenCalled()

    await user.clear(input)
    await user.type(input, '4294967296')
    await user.click(screen.getByRole('button', { name: '保存' }))
    expect(
      await screen.findByText(/推荐权重不能超过 4294967295/),
    ).toBeInTheDocument()
    expect(mockedPost).not.toHaveBeenCalled()
  })
})
