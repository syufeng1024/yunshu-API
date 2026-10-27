import { cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '@/api/client'
import type {
  BillResponse,
  ConsoleModel,
  ExportStatusResponse,
  UsageResponse,
  UsageRow,
} from '@/api/console'
import { renderWithProviders } from '@/test/renderConsole'
import UsagePage from './UsagePage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)
const mockedPost = vi.mocked(api.post)

const COLUMNS = [
  '维度',
  '维度ID',
  '名称',
  '请求数',
  '成功数',
  '成功率(基点)',
  '输入Token',
  '输出Token',
  '消耗微积分',
  '充值消耗微积分',
  '赠送消耗微积分',
  '金额(分)',
  '金额(元)',
]

function row(overrides: Partial<UsageRow> = {}): UsageRow {
  return {
    dimension: 'model',
    id: 11,
    name: 'Text Large',
    request_total: 100,
    request_success: 98,
    success_rate_bp: 9800,
    tokens_in: 150_000,
    tokens_out: 250_000,
    consumed_micro: 150_000_000,
    consumed_recharge_micro: 100_000_000,
    consumed_bonus_micro: 50_000_000,
    amount_fen: 15000,
    amount_yuan: '150.00',
    ...overrides,
  }
}

const totals = row({ dimension: 'total', id: 0, name: '合计' })

function usageResponse(params: URLSearchParams): UsageResponse {
  const dimension = (params.get('dimension') ?? 'model') as UsageResponse['dimension']
  const item = row({ dimension, name: dimension === 'day' ? '2026-10-27' : 'Text Large' })
  return {
    from: '2026-10-21T00:00:00Z',
    to: '2026-10-27T23:59:59Z',
    dimension,
    filter: {
      model_id: params.get('model_id') ? Number(params.get('model_id')) : null,
      channel_id: null,
    },
    window_days: 365,
    columns: COLUMNS,
    items: [item],
    totals: { ...totals, dimension: 'total' },
  }
}

const bill: BillResponse = {
  month: '2026-10',
  period_start: '2026-09-30T16:00:00Z',
  period_end: '2026-10-31T16:00:00Z',
  request_total: 1000,
  request_success: 985,
  success_rate_bp: 9850,
  consumed_micro: 123_450_000,
  consumed_recharge_micro: 100_000_000,
  consumed_bonus_micro: 23_450_000,
  amount_fen: 12345,
  amount_yuan: '123.45',
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

interface MockOptions {
  usageError?: ApiError
  billError?: ApiError
  exportError?: ApiError
  exportStatus?: ExportStatusResponse | ApiError
}

function installMock(options: MockOptions = {}) {
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/console/usage?')) {
      if (options.usageError) return Promise.reject(options.usageError)
      const params = new URL(path, 'http://localhost').searchParams
      return Promise.resolve(usageResponse(params))
    }
    if (path.startsWith('/api/console/bills')) {
      if (options.billError) return Promise.reject(options.billError)
      return Promise.resolve(bill)
    }
    if (path.startsWith('/api/console/exports/')) {
      if (options.exportStatus instanceof ApiError) return Promise.reject(options.exportStatus)
      return Promise.resolve(
        options.exportStatus ?? {
          task_id: 'task-1',
          state: 'DONE',
          url: '/api/console/exports/dl-1',
          expires_at: '2026-10-27T12:00:00Z',
        },
      )
    }
    if (path.startsWith('/api/console/models')) {
      return Promise.resolve({ items: models, page: 1, page_size: 100, total: models.length })
    }
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedPost.mockReset()
})

afterEach(cleanup)

describe('UsagePage', () => {
  it('F9-1: switching dimension keeps the time range and model filter', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<UsagePage />)

    const table = await screen.findByRole('table', { name: '用量明细' })
    expect(await within(table).findByText('Text Large')).toBeInTheDocument()
    await screen.findByRole('option', { name: 'Text Large' })
    const firstCall = mockedGet.mock.calls
      .map((call) => call[0] as string)
      .find((path) => path.startsWith('/api/console/usage?'))
    const firstParams = new URL(firstCall ?? '', 'http://localhost').searchParams
    const from = firstParams.get('from')
    const to = firstParams.get('to')

    await user.selectOptions(screen.getByLabelText('模型筛选'), '11')
    await waitFor(() => {
      const paths = mockedGet.mock.calls
        .map((call) => call[0] as string)
        .filter((path) => path.startsWith('/api/console/usage?'))
      const params = new URL(paths.at(-1) ?? '', 'http://localhost').searchParams
      expect(params.get('model_id')).toBe('11')
    })

    await user.click(screen.getByRole('button', { name: '按渠道' }))

    await waitFor(() => {
      const paths = mockedGet.mock.calls
        .map((call) => call[0] as string)
        .filter((path) => path.startsWith('/api/console/usage?'))
      const params = new URL(paths.at(-1) ?? '', 'http://localhost').searchParams
      expect(params.get('dimension')).toBe('channel')
      expect(params.get('model_id')).toBe('11')
      expect(params.get('from')).toBe(from)
      expect(params.get('to')).toBe(to)
    })
  })

  it('F9-2: the monthly bill shows both backend representations (12,345 积分 / 123.45 元)', async () => {
    installMock()
    renderWithProviders(<UsagePage />)

    expect(await screen.findByText('123.45 元')).toBeInTheDocument()
    expect(screen.getByText('12,345 积分')).toBeInTheDocument()
    expect(screen.getByText('10,000 积分')).toBeInTheDocument()
    expect(screen.getByText('2,345 积分')).toBeInTheDocument()
  })

  it('F9-3: table headers come from the same column list the CSV uses, and export carries the filters', async () => {
    installMock()
    mockedPost.mockResolvedValue({ task_id: 'task-1', state: 'RUNNING' })
    const user = userEvent.setup()
    renderWithProviders(<UsagePage />)

    const table = await screen.findByRole('table', { name: '用量明细' })
    await within(table).findByText('Text Large')
    const headers = within(table)
      .getAllByRole('columnheader')
      .map((header) => header.textContent)
    expect(headers).toEqual(COLUMNS)

    await user.click(screen.getByRole('button', { name: '导出 CSV' }))

    expect(await screen.findByRole('link', { name: '下载 CSV' })).toHaveAttribute(
      'href',
      '/api/console/exports/dl-1',
    )

    const exportPath = mockedPost.mock.calls[0][0] as string
    const params = new URL(exportPath, 'http://localhost').searchParams
    expect(params.get('dimension')).toBe('model')
    expect(params.get('from')).toBeTruthy()
    expect(params.get('to')).toBeTruthy()
  })

  it('surfaces the backend 409 message and hint verbatim when an export is already running', async () => {
    installMock()
    mockedPost.mockRejectedValue(
      new ApiError({
        status: 409,
        type: 'conflict',
        code: 'resource_state_conflict',
        message: '已有导出任务进行中',
        hint: '单个账号同时只能运行 1 个导出任务，请等待当前任务完成或 1 小时后重试',
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<UsagePage />)

    const table = await screen.findByRole('table', { name: '用量明细' })
    await within(table).findByText('Text Large')
    await user.click(screen.getByRole('button', { name: '导出 CSV' }))

    expect(await screen.findByText('已有导出任务进行中')).toBeInTheDocument()
    expect(
      screen.getByText('单个账号同时只能运行 1 个导出任务，请等待当前任务完成或 1 小时后重试'),
    ).toBeInTheDocument()
  })

  it('shows the 404 expiry message and hint when the export token is stale', async () => {
    installMock({
      exportStatus: new ApiError({
        status: 404,
        type: 'not_found',
        code: 'resource_not_found',
        message: '导出链接已失效',
        hint: '导出链接有效期为 1 小时，请重新发起导出',
      }),
    })
    mockedPost.mockResolvedValue({ task_id: 'task-1', state: 'RUNNING' })
    const user = userEvent.setup()
    renderWithProviders(<UsagePage />)

    const table = await screen.findByRole('table', { name: '用量明细' })
    await within(table).findByText('Text Large')
    await user.click(screen.getByRole('button', { name: '导出 CSV' }))

    expect(await screen.findByText('导出链接已失效')).toBeInTheDocument()
    expect(screen.getByText('导出链接有效期为 1 小时，请重新发起导出')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新发起导出' })).toBeInTheDocument()
  })

  it('renders the 400 window error with the backend message instead of a zero-filled chart', async () => {
    installMock({
      usageError: new ApiError({
        status: 400,
        type: 'invalid_request_error',
        code: 'invalid_query_parameter',
        message: '查询范围超出 365 天在线窗口',
        hint: '请使用归档导出获取更早的用量数据',
      }),
    })
    renderWithProviders(<UsagePage />)

    const messages = await screen.findAllByText('查询范围超出 365 天在线窗口')
    expect(messages.length).toBeGreaterThan(0)
    expect(screen.getAllByText('请使用归档导出获取更早的用量数据').length).toBeGreaterThan(0)
    expect(screen.queryByText('0 积分')).not.toBeInTheDocument()
  })
})
