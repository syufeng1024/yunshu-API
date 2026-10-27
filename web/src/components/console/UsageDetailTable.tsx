import { Table } from '@/components'
import type { TableColumn } from '@/components'
import type { UsageResponse, UsageRow } from '@/api/console'
import { formatInteger } from '@/lib/format'

/**
 * The backend's `columns` array and the CSV header are one definition
 * (internal/control/usage/query.go usageColumns). Rendering the headers from
 * the response keeps F9-3 true by construction instead of duplicating the
 * list in the bundle: the row field order is the same definition's JSON side.
 */
const ROW_FIELDS: readonly (keyof UsageRow)[] = [
  'dimension',
  'id',
  'name',
  'request_total',
  'request_success',
  'success_rate_bp',
  'tokens_in',
  'tokens_out',
  'consumed_micro',
  'consumed_recharge_micro',
  'consumed_bonus_micro',
  'amount_fen',
  'amount_yuan',
]

function cellText(row: UsageRow, field: keyof UsageRow | undefined): string {
  if (!field) return '—'
  const value = row[field]
  if (typeof value === 'number') return formatInteger(value)
  return value
}

export interface UsageDetailTableProps {
  data: UsageResponse | undefined
  loading: boolean
  error: string | null
  onRetry: () => void
}

export function UsageDetailTable({ data, loading, error, onRetry }: UsageDetailTableProps) {
  const columns: TableColumn<UsageRow>[] =
    data?.columns.map((header, index) => ({
      key: `usage-${index}`,
      header,
      align: index === 0 || index === 2 ? 'left' : 'right',
      render: (row) => (
        <span className={index === 2 ? 'text-ink' : 'tabular-nums text-ink-secondary'}>
          {cellText(row, ROW_FIELDS[index])}
        </span>
      ),
    })) ?? []

  return (
    <Table
      ariaLabel="用量明细"
      columns={columns}
      rows={data?.items ?? []}
      rowKey={(row) => `${row.dimension}-${row.id}-${row.name}`}
      loading={loading}
      error={error}
      onRetry={onRetry}
      emptyTitle="当前筛选范围无用量"
      emptyDescription="调整时间范围或模型筛选后重试。"
    />
  )
}
