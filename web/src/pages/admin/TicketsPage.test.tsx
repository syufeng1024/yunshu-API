import { cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { AdminTicketItem } from '@/api/admin'
import { useToastStore } from '@/stores/toast'
import { renderWithProviders } from '@/test/renderConsole'
import AdminTicketsPage from './TicketsPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)
const mockedPost = vi.mocked(api.post)

function ticket(overrides: Partial<AdminTicketItem> = {}): AdminTicketItem {
  return {
    id: 1,
    ticket_no: 'TK2026102800010001',
    user_id: 501,
    user_email: 'ops-demo@example.com',
    ticket_type: 'TECH',
    title: '流式响应中断',
    request_id: 'req_20261028_7f3a',
    policy_hit_id: null,
    state: 'OPEN',
    first_response_due_at: '2026-10-28T04:00:00Z',
    first_responded_at: null,
    last_replied_at: null,
    created_at: '2026-10-28T00:00:00Z',
    sla: {
      first_response_due_at: '2026-10-28T04:00:00Z',
      first_responded_at: null,
      responded: false,
      overdue: false,
      remaining_seconds: 3600,
      overdue_seconds: 0,
      display: '剩余 1 小时',
    },
    ...overrides,
  }
}

const auditItems = [
  {
    id: 1,
    trace_id: 'trace-1',
    operator_id: '9001',
    operator_role: 'OPS',
    operator_ip: '10.0.0.1',
    module: 'TICKET',
    action: 'REPLY',
    target_type: 't_ticket',
    target_id: '1',
    before: { state: 'OPEN' },
    after: { state: 'RESPONDED' },
    result: 'OK',
    created_at: '2026-10-28T10:00:00Z',
  },
  {
    id: 2,
    trace_id: 'trace-2',
    operator_id: '9001',
    operator_role: 'OPS',
    operator_ip: '10.0.0.1',
    module: 'TICKET',
    action: 'INTERNAL_NOTE',
    target_type: 't_ticket',
    target_id: '1',
    before: { state: 'RESPONDED' },
    after: { state: 'RESPONDED', is_internal: true },
    result: 'OK',
    created_at: '2026-10-28T10:01:00Z',
  },
  {
    id: 3,
    trace_id: 'trace-3',
    operator_id: '9001',
    operator_role: 'OPS',
    operator_ip: '10.0.0.1',
    module: 'TICKET',
    action: 'STATE_CHANGE',
    target_type: 't_ticket',
    target_id: '1',
    before: { state: 'RESPONDED' },
    after: { state: 'CLOSED' },
    result: 'OK',
    created_at: '2026-10-28T10:02:00Z',
  },
]

const auditPage = {
  items: auditItems,
  page: 1,
  page_size: 50,
  total: auditItems.length,
  from: '2026-10-28T00:00:00Z',
  to: '2026-10-28T23:59:59Z',
}

let currentTicket: AdminTicketItem
let replySequence = 0

function installMock(initial: AdminTicketItem) {
  currentTicket = initial
  replySequence = 0
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/admin/tickets?')) {
      return Promise.resolve({
        items: [currentTicket],
        page: 1,
        page_size: 20,
        total: 1,
      })
    }
    if (path === '/api/admin/tickets/1') {
      return Promise.resolve({
        ...currentTicket,
        description: '服务端工单正文',
        policy_rule_id: null,
        policy_category: null,
        reopen_count: 0,
        updated_at: currentTicket.created_at,
        replies: [
          {
            id: 9001,
            author_type: 'USER',
            content: '用户的历史回复',
            created_at: '2026-10-28T09:00:00Z',
            is_internal: false,
          },
          {
            id: 9002,
            author_type: 'OPS',
            content: '历史内部备注',
            created_at: '2026-10-28T09:05:00Z',
            is_internal: true,
          },
        ],
      })
    }
    if (path.startsWith('/api/admin/audit-logs?')) return Promise.resolve(auditPage)
    return Promise.reject(new Error(`unexpected ${path}`))
  })

  mockedPost.mockImplementation(((path: string, body?: unknown) => {
    if (path.endsWith('/reply')) {
      const input = body as { content: string; is_internal?: boolean }
      const reply = {
        id: (replySequence += 1),
        author_type: 'OPS',
        content: input.content,
        created_at: '2026-10-28T10:00:00Z',
      }
      if (input.is_internal) {
        currentTicket = { ...currentTicket, last_replied_at: reply.created_at }
      } else {
        currentTicket = {
          ...currentTicket,
          state: 'RESPONDED',
          first_responded_at: reply.created_at,
          last_replied_at: reply.created_at,
          sla: {
            ...currentTicket.sla,
            first_responded_at: reply.created_at,
            responded: true,
            overdue: false,
            remaining_seconds: 0,
            overdue_seconds: 0,
            display: '已响应（用时 1 小时）',
          },
        }
      }
      return Promise.resolve({ reply, ticket: currentTicket })
    }
    if (path.endsWith('/state')) {
      currentTicket = { ...currentTicket, state: 'CLOSED' }
      return Promise.resolve(currentTicket)
    }
    return Promise.reject(new Error(`unexpected POST ${path}`))
  }) as never)
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedPost.mockReset()
  useToastStore.setState({ toasts: [] })
})

afterEach(cleanup)

async function openTicketDrawer() {
  const user = userEvent.setup()
  renderWithProviders(<AdminTicketsPage />)
  await screen.findByText('TK2026102800010001')
  const table = screen.getByRole('table', { name: '工单队列' })
  const row = within(table).getByText('TK2026102800010001').closest('tr')
  await user.click(within(row as HTMLElement).getByRole('button', { name: '处理' }))
  await screen.findByLabelText('回复内容')
  return user
}

async function closeAuditDrawer(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByTestId('audit-trail')
  const closeButtons = screen.getAllByRole('button', { name: '关闭抽屉' })
  await user.click(closeButtons[closeButtons.length - 1])
  await waitFor(() => expect(screen.queryByTestId('audit-trail')).not.toBeInTheDocument())
}

function ticketRow() {
  const table = screen.getByRole('table', { name: '工单队列' })
  const cell = within(table).getByText('TK2026102800010001')
  return cell.closest('tr') as HTMLElement
}

describe('TicketsPage', () => {
  it('TicketsPage：回复后状态变已响应', async () => {
    installMock(ticket())
    const user = await openTicketDrawer()

    expect(await screen.findByText('服务端工单正文')).toBeInTheDocument()
    expect(screen.getByText('历史内部备注')).toBeInTheDocument()

    await user.type(screen.getByLabelText('回复内容'), '已处理您的请求')
    await user.click(screen.getByRole('button', { name: '回复用户' }))

    await waitFor(() => expect(mockedPost).toHaveBeenCalledTimes(1))
    expect(mockedPost.mock.calls[0]?.[1]).toEqual({
      content: '已处理您的请求',
      is_internal: false,
    })

    await waitFor(() => {
      expect(within(ticketRow()).getByText('已响应')).toBeInTheDocument()
    })
  })

  it('TicketsPage：超时不自动关闭', async () => {
    installMock(
      ticket({
        sla: {
          first_response_due_at: '2026-10-28T04:00:00Z',
          first_responded_at: null,
          responded: false,
          overdue: true,
          remaining_seconds: 0,
          overdue_seconds: 1800,
          display: '已超时 30 分钟',
        },
      }),
    )
    renderWithProviders(<AdminTicketsPage />)

    expect(await screen.findByText('已超时 30 分钟')).toBeInTheDocument()
    expect(within(ticketRow()).getByText('待响应')).toBeInTheDocument()
    expect(screen.queryByText('自动关闭')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '自动关闭' })).not.toBeInTheDocument()
    expect(screen.queryByText('AUTO_CLOSED')).not.toBeInTheDocument()
  })

  it('TicketsPage：内部备注不改状态', async () => {
    installMock(ticket())
    const user = await openTicketDrawer()

    expect(screen.getByText('历史内部备注')).toBeInTheDocument()
    expect(screen.getAllByText('内部备注').length).toBeGreaterThan(0)

    await user.type(screen.getByLabelText('回复内容'), '内部备注内容')
    await user.click(screen.getByRole('button', { name: '内部备注' }))
    await user.click(screen.getByRole('button', { name: '确认提交内部备注' }))

    await waitFor(() => expect(mockedPost).toHaveBeenCalledTimes(1))
    expect(mockedPost.mock.calls[0]?.[1]).toEqual({
      content: '内部备注内容',
      is_internal: true,
    })
    await waitFor(() => {
      expect(within(ticketRow()).getByText('待响应')).toBeInTheDocument()
    })
    expect(screen.getByText('本次会话新建')).toBeInTheDocument()
  })

  it('TicketsPage：三条写操作后审计可查', async () => {
    installMock(ticket())
    const user = await openTicketDrawer()

    // 1) public reply
    await user.type(screen.getByLabelText('回复内容'), '第一次回复')
    await user.click(screen.getByRole('button', { name: '回复用户' }))
    await closeAuditDrawer(user)

    // 2) internal note
    await user.type(screen.getByLabelText('回复内容'), '内部备注')
    await user.click(screen.getByRole('button', { name: '内部备注' }))
    await user.click(screen.getByRole('button', { name: '确认提交内部备注' }))
    await closeAuditDrawer(user)

    // 3) manual close
    await user.click(screen.getByRole('button', { name: '关闭工单' }))
    await user.click(screen.getByRole('button', { name: '确认关闭' }))
    await screen.findByTestId('audit-trail')

    const paths = mockedPost.mock.calls.map((call) => call[0])
    expect(paths.filter((path) => path.endsWith('/reply'))).toHaveLength(2)
    expect(paths.filter((path) => path.endsWith('/state'))).toHaveLength(1)
    expect(await screen.findAllByTestId('audit-log-item')).toHaveLength(3)
    expect(screen.getByText(/TICKET 模块/)).toBeInTheDocument()
  })
})
