import { useEffect, useMemo, useState } from 'react'
import { ApiError, errorHint, errorMessage } from '@/api/client'
import {
  useTickets,
  type TicketListItem,
  type TicketState,
  type TicketType,
} from '@/api/console'
import {
  Badge,
  Button,
  ErrorState,
  PageHeader,
  Select,
  Table,
} from '@/components'
import type { TableColumn } from '@/components'
import { TicketCreateDialog, TicketDetailDrawer } from '@/components/console'
import { ticketStateLabel, ticketTypeLabel } from '@/lib/consoleLabels'
import { formatDateTimeMs } from '@/lib/format'
import { usePageTitle } from '@/lib/usePageTitle'

const PAGE_SIZE = 20

const STATE_OPTIONS: readonly { value: TicketState | 'all'; label: string }[] = [
  { value: 'all', label: '全部状态' },
  { value: 'OPEN', label: ticketStateLabel('OPEN') },
  { value: 'RESPONDED', label: ticketStateLabel('RESPONDED') },
  { value: 'CLOSED', label: ticketStateLabel('CLOSED') },
]

const TYPE_OPTIONS: readonly { value: TicketType | 'all'; label: string }[] = [
  { value: 'all', label: '全部类型' },
  { value: 'BILLING', label: ticketTypeLabel('BILLING') },
  { value: 'TECH', label: ticketTypeLabel('TECH') },
  { value: 'CONTENT_APPEAL', label: ticketTypeLabel('CONTENT_APPEAL') },
  { value: 'OTHER', label: ticketTypeLabel('OTHER') },
]

function stateVariant(state: TicketState): 'warning' | 'primary' | 'neutral' {
  if (state === 'OPEN') return 'warning'
  if (state === 'RESPONDED') return 'primary'
  return 'neutral'
}

function buildColumns(
  onOpen: (id: number) => void,
): TableColumn<TicketListItem>[] {
  return [
    {
      key: 'ticket_no',
      header: '工单号',
      render: (row) => <span className="font-mono text-xs">{row.ticket_no}</span>,
    },
    {
      key: 'ticket_type',
      header: '类型',
      render: (row) => (
        <span className="whitespace-nowrap text-xs">
          {ticketTypeLabel(row.ticket_type)}
        </span>
      ),
    },
    {
      key: 'title',
      header: '标题',
      render: (row) => <span className="text-xs text-ink">{row.title}</span>,
    },
    {
      key: 'state',
      header: '状态',
      render: (row) => (
        <Badge variant={stateVariant(row.state)}>{ticketStateLabel(row.state)}</Badge>
      ),
    },
    {
      key: 'sla',
      header: '响应进度',
      render: (row) => (
        <div className="space-y-1">
          <p className="whitespace-nowrap text-xs text-ink-secondary">
            {row.sla.display}
          </p>
          {row.sla.overdue ? <Badge variant="danger">SLA 超时</Badge> : null}
        </div>
      ),
    },
    {
      key: 'created_at',
      header: '创建时间',
      render: (row) => (
        <span className="whitespace-nowrap tabular-nums text-xs">
          {formatDateTimeMs(row.created_at)}
        </span>
      ),
    },
    {
      key: 'actions',
      header: '操作',
      align: 'right',
      render: (row) => (
        <Button
          size="sm"
          variant="ghost"
          aria-label={`查看工单 ${row.ticket_no}`}
          onClick={() => onOpen(row.id)}
        >
          详情
        </Button>
      ),
    },
  ]
}

export default function TicketsPage() {
  usePageTitle('工单中心')

  const [stateFilter, setStateFilter] = useState<TicketState | 'all'>('all')
  const [typeFilter, setTypeFilter] = useState<TicketType | 'all'>('all')
  const [page, setPage] = useState(1)
  const [createOpen, setCreateOpen] = useState(false)
  const [selectedTicketId, setSelectedTicketId] = useState<number | null>(null)

  useEffect(() => {
    setPage(1)
  }, [stateFilter, typeFilter])

  const params = useMemo(
    () => ({
      state: stateFilter,
      type: typeFilter,
      page,
      pageSize: PAGE_SIZE,
    }),
    [stateFilter, typeFilter, page],
  )

  const tickets = useTickets(params)
  const columns = useMemo(() => buildColumns(setSelectedTicketId), [])

  const listError = tickets.isError ? errorMessage(tickets.error) : null
  const listHint = tickets.isError ? errorHint(tickets.error) : null
  const forbidden =
    tickets.isError && tickets.error instanceof ApiError && tickets.error.status === 403
  const filtered = stateFilter !== 'all' || typeFilter !== 'all'

  return (
    <div className="space-y-5">
      <PageHeader
        title="工单中心"
        description="提交计费、技术或内容申诉工单，跟踪首次响应与答复进度。"
        actions={<Button onClick={() => setCreateOpen(true)}>新建工单</Button>}
      />

      <div className="grid gap-3 sm:grid-cols-2 lg:w-1/2">
        <Select
          label="状态"
          value={stateFilter}
          onChange={(event) => setStateFilter(event.target.value as TicketState | 'all')}
          options={STATE_OPTIONS}
        />
        <Select
          label="类型"
          value={typeFilter}
          onChange={(event) => setTypeFilter(event.target.value as TicketType | 'all')}
          options={TYPE_OPTIONS}
        />
      </div>

      {listError ? (
        <ErrorState
          title={forbidden ? '无权限' : undefined}
          message={listError}
          hint={listHint}
          onRetry={() => void tickets.refetch()}
        />
      ) : (
        <Table
          ariaLabel="工单列表"
          columns={columns}
          rows={tickets.data?.items ?? []}
          rowKey={(row) => String(row.id)}
          loading={tickets.isLoading}
          emptyTitle="暂无工单"
          emptyDescription={
            filtered
              ? '当前状态与类型筛选组合没有匹配的工单。'
              : '遇到计费、技术或内容申诉问题时，可以提交工单。'
          }
          emptyAction={
            filtered ? undefined : (
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                新建工单
              </Button>
            )
          }
          pagination={
            tickets.data
              ? {
                  page: tickets.data.page,
                  pageSize: tickets.data.page_size,
                  total: tickets.data.total,
                  onPageChange: setPage,
                }
              : undefined
          }
        />
      )}

      {createOpen ? (
        <TicketCreateDialog open onClose={() => setCreateOpen(false)} />
      ) : null}

      <TicketDetailDrawer
        ticketId={selectedTicketId}
        onClose={() => setSelectedTicketId(null)}
      />
    </div>
  )
}
