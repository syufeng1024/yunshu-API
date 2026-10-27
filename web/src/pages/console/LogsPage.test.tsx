import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, api } from '@/api/client'
import type {
  ConsoleModel,
  LogAttempt,
  LogBodyResponse,
  LogDetailRequest,
  LogDetailResponse,
  LogItem,
  LogListResponse,
} from '@/api/console'
import { renderWithProviders } from '@/test/renderConsole'
import LogsPage from './LogsPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)

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

const REQUEST_ID = 'req-retry-1'

function logItem(overrides: Partial<LogItem> = {}): LogItem {
  return {
    request_id: REQUEST_ID,
    api_key_id: 7,
    api_key_name: 'prod-key',
    model_id: 11,
    model_name: 'text-large',
    request_model: 'text-large',
    inbound_protocol: 'OPENAI_COMPAT',
    channel_id: 3,
    channel_name: 'primary',
    group_code: 'standard',
    is_stream: true,
    http_status: 502,
    biz_code: 'upstream_error',
    result: 'FAILED',
    fail_stage: 'UPSTREAM',
    fail_owner: 'UPSTREAM',
    tokens_in: 120,
    tokens_out: 0,
    consumed_micro: 0,
    upstream_attempts: 2,
    ttft_ms: null,
    total_ms: 830,
    upstream_ms: 810,
    client_ip: '203.0.113.7',
    client_aborted: false,
    full_log_saved: true,
    created_at: '2026-10-27T08:00:00.123Z',
    ...overrides,
  }
}

function logPage(items: LogItem[]): LogListResponse {
  return {
    items,
    page: 1,
    page_size: 20,
    total: items.length,
    from: '2026-10-21T00:00:00Z',
    to: '2026-10-27T23:59:59Z',
    window_days: 365,
  }
}

function detailRequest(overrides: Partial<LogDetailRequest> = {}): LogDetailRequest {
  return {
    ...logItem(),
    outbound_protocol: 'OPENAI_COMPAT',
    prehold_micro: 5_000,
    consumed_recharge_micro: 0,
    consumed_bonus_micro: 0,
    ...overrides,
  }
}

function attempt(overrides: Partial<LogAttempt> = {}): LogAttempt {
  return {
    attempt_index: 1,
    result: 'FAILED',
    http_status: 502,
    phase: 'FIRST_BYTE',
    channel_id: 3,
    channel_name: 'primary',
    protocol_code: 'OPENAI_COMPAT',
    upstream_model: 'upstream-large',
    connect_ms: 12,
    first_byte_ms: null,
    total_ms: 830,
    tokens_in: 0,
    tokens_out: 0,
    is_retryable: true,
    retry_decision: 'RETRY_NEXT_ROUTE',
    error_class: 'UPSTREAM_5XX',
    upstream_code: 'bad_gateway',
    upstream_msg: 'upstream returned 502',
    created_at: '2026-10-27T08:00:00.123Z',
    ...overrides,
  }
}

function detail(
  request: LogDetailRequest,
  timeline: LogAttempt[],
): LogDetailResponse {
  return { request, timeline }
}

const modelsPage = {
  items: models,
  page: 1,
  page_size: 100,
  total: models.length,
}

interface MockOptions {
  logs?: LogListResponse | ApiError
  detail?: LogDetailResponse | ApiError
  body?: LogBodyResponse | ApiError
}

function installMock(options: MockOptions = {}) {
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/console/logs/') && path.endsWith('/body')) {
      if (options.body instanceof ApiError) return Promise.reject(options.body)
      return Promise.resolve(
        options.body ?? {
          request_id: REQUEST_ID,
          model_name: 'text-large',
          created_at: '2026-10-27T08:00:00.123Z',
          body: '{"messages":[]}',
          redacted: true,
        },
      )
    }
    if (path.startsWith('/api/console/logs/')) {
      if (options.detail instanceof ApiError) return Promise.reject(options.detail)
      return Promise.resolve(
        options.detail ??
          detail(detailRequest(), [
            attempt(),
            attempt({
              attempt_index: 2,
              result: 'SUCCESS',
              http_status: 200,
              phase: 'COMPLETE',
              connect_ms: 8,
              first_byte_ms: 120,
              total_ms: 900,
              is_retryable: false,
              retry_decision: 'NO_RETRY',
              error_class: null,
              upstream_code: null,
              upstream_msg: null,
            }),
          ]),
      )
    }
    if (path.startsWith('/api/console/logs?')) {
      if (options.logs instanceof ApiError) return Promise.reject(options.logs)
      return Promise.resolve(options.logs ?? logPage([logItem()]))
    }
    if (path.startsWith('/api/console/models?')) {
      return Promise.resolve(modelsPage)
    }
    return Promise.reject(new Error(`unexpected ${path}`))
  })
}

function lastLogsParams(): URLSearchParams {
  const paths = mockedGet.mock.calls
    .map((call) => call[0] as string)
    .filter((path) => path.startsWith('/api/console/logs?'))
  return new URL(paths.at(-1) ?? '', 'http://localhost').searchParams
}

beforeEach(() => {
  mockedGet.mockReset()
})

afterEach(cleanup)

describe('LogsPage', () => {
  it('F11-1: time, state and model filters are sent in the same request (intersection)', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<LogsPage />)

    await screen.findByRole('table', { name: '请求日志列表' })
    await screen.findByRole('option', { name: 'Text Large' })

    fireEvent.change(screen.getByLabelText('开始日期'), {
      target: { value: '2026-10-01' },
    })
    fireEvent.change(screen.getByLabelText('结束日期'), {
      target: { value: '2026-10-07' },
    })
    await user.selectOptions(screen.getByLabelText('状态'), 'failed')
    await user.selectOptions(screen.getByLabelText('模型筛选'), 'text-large')

    await waitFor(() => {
      const params = lastLogsParams()
      expect(params.get('state')).toBe('failed')
      expect(params.get('model')).toBe('text-large')
      expect(params.get('from')).toBe('2026-10-01')
      expect(params.get('to')).toBe('2026-10-07')
      expect(params.get('page')).toBe('1')
    })
  })

  it('F11-1: a 365-day window rejection shows the backend message and hint', async () => {
    installMock({
      logs: new ApiError({
        status: 400,
        type: 'invalid_request_error',
        code: 'invalid_query_parameter',
        message: '查询范围超出 365 天在线窗口',
        hint: '请使用归档导出获取更早的请求日志',
      }),
    })
    renderWithProviders(<LogsPage />)

    expect(await screen.findByText('查询范围超出 365 天在线窗口')).toBeInTheDocument()
    expect(screen.getByText('请使用归档导出获取更早的请求日志')).toBeInTheDocument()
    expect(
      screen.queryByRole('table', { name: '请求日志列表' }),
    ).not.toBeInTheDocument()
  })

  it('F11-2: one retry renders two attempt rows with their own latency and result', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<LogsPage />)

    const failedRow = (await screen.findByText(REQUEST_ID)).closest('tr')
    expect(failedRow?.className).toContain('border-l-danger')
    expect(screen.getByText('阶段：上游调用')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: `查看请求 ${REQUEST_ID}` }))

    const dialog = await screen.findByRole('dialog', { name: '请求详情' })
    const timeline = await within(dialog).findByRole('list', { name: '重试时间线' })
    const attempts = within(timeline).getAllByRole('listitem')
    expect(attempts).toHaveLength(2)

    expect(within(attempts[0]).getByText('第 1 次尝试')).toBeInTheDocument()
    expect(within(attempts[0]).getByText('HTTP 502')).toBeInTheDocument()
    expect(within(attempts[0]).getByText('830 ms')).toBeInTheDocument()
    expect(within(attempts[0]).getByText('12 ms')).toBeInTheDocument()
    expect(within(attempts[0]).getByText('切换路由重试')).toBeInTheDocument()

    expect(within(attempts[1]).getByText('第 2 次尝试')).toBeInTheDocument()
    expect(within(attempts[1]).getByText('HTTP 200')).toBeInTheDocument()
    expect(within(attempts[1]).getByText('900 ms')).toBeInTheDocument()
    expect(within(attempts[1]).getByText('120 ms')).toBeInTheDocument()
    expect(within(attempts[1]).getByText('成功')).toBeInTheDocument()
  })

  it('F11-2: an empty timeline shows the fail stage instead of a blank section', async () => {
    installMock({
      detail: detail(detailRequest({ fail_stage: 'ROUTE', fail_owner: 'PLATFORM' }), []),
    })
    const user = userEvent.setup()
    renderWithProviders(<LogsPage />)

    await user.click(
      await screen.findByRole('button', { name: `查看请求 ${REQUEST_ID}` }),
    )
    const dialog = await screen.findByRole('dialog', { name: '请求详情' })

    expect(await within(dialog).findByText(/未产生上游尝试/)).toBeInTheDocument()
    expect(within(dialog).getByText(/失败阶段：路由选路/)).toBeInTheDocument()
  })

  it('F11-3: body 404 “not enabled or outside retention” keeps the exact message and hint', async () => {
    installMock({
      body: new ApiError({
        status: 404,
        type: 'invalid_request_error',
        code: 'resource_not_found',
        message: '正文未开启或不在此保留期内',
        hint: '完整日志需在账号设置中开启；正文仅保留 180 天',
      }),
    })
    const user = userEvent.setup()
    renderWithProviders(<LogsPage />)

    await user.click(
      await screen.findByRole('button', { name: `查看请求 ${REQUEST_ID}` }),
    )
    const dialog = await screen.findByRole('dialog', { name: '请求详情' })
    await user.click(within(dialog).getByRole('button', { name: '加载正文' }))

    expect(
      await within(dialog).findByText('正文未开启或不在此保留期内'),
    ).toBeInTheDocument()
    expect(
      within(dialog).getByText('完整日志需在账号设置中开启；正文仅保留 180 天'),
    ).toBeInTheDocument()
    expect(within(dialog).queryByText('无数据')).not.toBeInTheDocument()
  })

  it('F11-3: the ownership 404 is also rendered verbatim, never as empty data', async () => {
    installMock({
      body: new ApiError({
        status: 404,
        type: 'invalid_request_error',
        code: 'resource_not_found',
        message: '资源不存在或已被删除',
        hint: '刷新列表后重试',
      }),
    })
    const user = userEvent.setup()
    renderWithProviders(<LogsPage />)

    await user.click(
      await screen.findByRole('button', { name: `查看请求 ${REQUEST_ID}` }),
    )
    const dialog = await screen.findByRole('dialog', { name: '请求详情' })
    await user.click(within(dialog).getByRole('button', { name: '加载正文' }))

    expect(await within(dialog).findByText('资源不存在或已被删除')).toBeInTheDocument()
    expect(within(dialog).getByText('刷新列表后重试')).toBeInTheDocument()
    expect(within(dialog).queryByText('无数据')).not.toBeInTheDocument()
  })

  it('F11-3: a stored body renders its content together with the redaction marker', async () => {
    installMock()
    const user = userEvent.setup()
    renderWithProviders(<LogsPage />)

    await user.click(
      await screen.findByRole('button', { name: `查看请求 ${REQUEST_ID}` }),
    )
    const dialog = await screen.findByRole('dialog', { name: '请求详情' })
    await user.click(within(dialog).getByRole('button', { name: '加载正文' }))

    expect(await within(dialog).findByText('敏感字段已脱敏')).toBeInTheDocument()
    expect(within(dialog).getByText('{"messages":[]}')).toBeInTheDocument()
  })
})
