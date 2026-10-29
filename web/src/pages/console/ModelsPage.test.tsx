import { cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { CatalogModel, ModelCatalogResponse } from '@/api/console'
import { renderWithProviders } from '@/test/renderConsole'
import ModelsPage from './ModelsPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)

function model(overrides: Partial<CatalogModel> = {}): CatalogModel {
  return {
    id: 11,
    model_name: 'text-large',
    display_name: 'Text Large',
    model_type: 'TEXT',
    context_length: 128_000,
    protocol: 'OPENAI_COMPAT',
    aliases: [],
    capabilities: { stream: true, tool_call: null, vision: null, json_mode: null },
    recommend_weight: 10,
    published_at: '2026-10-01T00:00:00Z',
    pricing: {
      in_price_micro_per_k: 1500,
      out_price_micro_per_k: 3000,
      per_call_price_micro: 0,
      in_price_credits_per_k: '0.15',
      out_price_credits_per_k: '0.30',
      per_call_credits_per_call: '0',
      in_price_cny_per_k: '0.15',
      out_price_cny_per_k: '0.30',
      per_call_cny_per_call: '0',
      cache_billing_enabled: false,
      cache_hit_ratio_bp: 5000,
      cache_write_ratio_bp: 2500,
      cache_hit_price_credits_per_k: '0.05',
      cache_write_price_credits_per_k: '0.10',
    },
    ...overrides,
  }
}

function catalog(items: CatalogModel[]): ModelCatalogResponse {
  return { items, page: 1, page_size: 20, total: items.length }
}

const allModels: CatalogModel[] = [
  model(),
  model({ id: 12, model_name: 'image-large', display_name: 'Image Large', protocol: 'BAILIAN' }),
]

interface MockOptions {
  catalogFor?: (params: URLSearchParams) => ModelCatalogResponse
}

function installMock(options: MockOptions = {}) {
  mockedGet.mockImplementation((path: string) => {
    const params = new URL(path, 'http://localhost').searchParams
    if (path.startsWith('/api/console/models') && params.get('page_size') === '100') {
      return Promise.resolve({ items: allModels, page: 1, page_size: 100, total: allModels.length })
    }
    if (path.startsWith('/api/console/models')) {
      return Promise.resolve(options.catalogFor?.(params) ?? catalog([model()]))
    }
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

beforeEach(() => {
  mockedGet.mockReset()
})

afterEach(cleanup)

describe('ModelsPage', () => {
  it('F8-2: renders micro-credit prices through the shared formatter and never a cache price', async () => {
    installMock()
    renderWithProviders(<ModelsPage />)

    expect(await screen.findByText('Text Large')).toBeInTheDocument()
    expect(screen.getByText('输入 0.15 积分/千 token')).toBeInTheDocument()
    expect(screen.getByText('输出 0.3 积分/千 token')).toBeInTheDocument()
    expect(screen.getByText('参考 0.15 / 0.30 元/千 token')).toBeInTheDocument()
    // cache_billing_enabled=false: the backend omits cache fields; the badge
    // set must not grow one here even if a proxy sends them.
    expect(screen.queryByText(/缓存/)).not.toBeInTheDocument()
  })

  it('F8-1: the context column formats the backend context_length (32768 → 32,768 tokens)', async () => {
    installMock({ catalogFor: () => catalog([model({ context_length: 32_768 })]) })
    renderWithProviders(<ModelsPage />)

    expect(await screen.findByText('32,768 tokens')).toBeInTheDocument()
  })

  it('F8-3: stream and tool_call badges follow the capability flags exactly', async () => {
    installMock({
      catalogFor: () =>
        catalog([
          model({
            id: 1,
            model_name: 'stream-only',
            display_name: '仅流式',
            capabilities: { stream: true, tool_call: false, vision: null, json_mode: null },
          }),
          model({
            id: 2,
            model_name: 'tool-only',
            display_name: '仅工具',
            capabilities: { stream: false, tool_call: true, vision: null, json_mode: null },
          }),
          model({
            id: 3,
            model_name: 'both-caps',
            display_name: '双能力',
            capabilities: { stream: true, tool_call: true, vision: null, json_mode: null },
          }),
          model({
            id: 4,
            model_name: 'no-caps',
            display_name: '无能力',
            capabilities: { stream: false, tool_call: false, vision: null, json_mode: null },
          }),
        ]),
    })
    renderWithProviders(<ModelsPage />)

    const table = await screen.findByRole('table', { name: '模型广场列表' })
    await screen.findByText('仅流式')
    const rowFor = (name: string) => within(table).getByText(name).closest('tr') as HTMLElement

    const onlyStream = rowFor('仅流式')
    expect(within(onlyStream).getByText('流式')).toHaveClass('bg-success-soft')
    expect(within(onlyStream).getByText('工具调用：不支持')).toHaveClass('bg-surface-muted')
    expect(within(onlyStream).queryByText('工具调用')).not.toBeInTheDocument()

    const onlyTool = rowFor('仅工具')
    expect(within(onlyTool).getByText('工具调用')).toHaveClass('bg-success-soft')
    expect(within(onlyTool).getByText('流式：不支持')).toHaveClass('bg-surface-muted')
    expect(within(onlyTool).queryByText('流式')).not.toBeInTheDocument()

    const both = rowFor('双能力')
    expect(within(both).getByText('流式')).toHaveClass('bg-success-soft')
    expect(within(both).getByText('工具调用')).toHaveClass('bg-success-soft')
    expect(within(both).queryByText(/不支持/)).not.toBeInTheDocument()

    const neither = rowFor('无能力')
    expect(within(neither).getByText('流式：不支持')).toHaveClass('bg-surface-muted')
    expect(within(neither).getByText('工具调用：不支持')).toHaveClass('bg-surface-muted')
    expect(within(neither).queryByText('流式')).not.toBeInTheDocument()

    expect(screen.getAllByText('流式')).toHaveLength(2)
    expect(screen.getAllByText('工具调用')).toHaveLength(2)
  })

  it('F8-1 / F8-4: type and protocol filters go to the API and the response order is preserved', async () => {
    const beta = model({ id: 2, model_name: 'beta', display_name: 'Beta', recommend_weight: 1 })
    const alpha = model({ id: 1, model_name: 'alpha', display_name: 'Alpha', recommend_weight: 99 })
    const gamma = model({
      id: 3,
      model_name: 'gamma-image',
      display_name: 'Gamma',
      model_type: 'IMAGE',
      protocol: 'BAILIAN',
      recommend_weight: 5,
    })
    installMock({
      catalogFor: (params) => {
        if (params.get('model_type') === 'IMAGE') return catalog([gamma, alpha])
        return catalog([beta, alpha])
      },
    })

    const user = userEvent.setup()
    renderWithProviders(<ModelsPage />)

    const table = await screen.findByRole('table', { name: '模型广场列表' })
    await screen.findByText('Beta')
    const initialRows = within(table).getAllByRole('row').slice(1)
    expect(initialRows[0]).toHaveTextContent('Beta')
    expect(initialRows[1]).toHaveTextContent('Alpha')

    await screen.findByRole('option', { name: '百炼' })
    await user.selectOptions(screen.getByLabelText('模型类型'), 'IMAGE')
    await user.selectOptions(screen.getByLabelText('厂商 / 协议'), 'BAILIAN')

    await screen.findByText('Gamma')
    await waitFor(() => {
      const last = mockedGet.mock.calls
        .map((call) => call[0] as string)
        .filter((path) => path.startsWith('/api/console/models') && !path.includes('page_size=100'))
        .at(-1)
      const params = new URL(last ?? '', 'http://localhost').searchParams
      expect(params.get('model_type')).toBe('IMAGE')
      expect(params.get('protocol')).toBe('BAILIAN')
      expect(params.get('page')).toBe('1')
    })

    const filteredRows = within(table).getAllByRole('row').slice(1)
    expect(filteredRows[0]).toHaveTextContent('Gamma')
    expect(filteredRows[1]).toHaveTextContent('Alpha')
  })

  it('F8-3: an empty backend catalog renders the empty state instead of placeholder models', async () => {
    installMock({ catalogFor: () => catalog([]) })
    renderWithProviders(<ModelsPage />)

    expect(await screen.findByText('暂无已上架模型')).toBeInTheDocument()
    expect(screen.getByText('后端当前没有对外展示的模型。')).toBeInTheDocument()
    expect(screen.queryByText('CATALOG_ONLY')).not.toBeInTheDocument()
  })

  it('renders a missing price as 未配置, never as a fake zero', async () => {
    installMock({ catalogFor: () => catalog([model({ pricing: null })]) })
    renderWithProviders(<ModelsPage />)

    expect(await screen.findByText('价格未配置')).toBeInTheDocument()
    expect(screen.queryByText(/0 积分/)).not.toBeInTheDocument()
  })
})
