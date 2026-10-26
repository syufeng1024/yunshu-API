import { useMemo } from 'react'
import type { PublicPlan } from '@/api/public'
import { Table } from '@/components'
import type { TableColumn } from '@/components'
import { formatInteger, formatPlanPrice, formatQuotaCredits } from '@/lib/format'
import { selectPurchasablePlans } from '@/lib/site'

export interface PlanLimitTableProps {
  plans: readonly PublicPlan[]
  loading?: boolean
  error?: string | null
  onRetry?: () => void
  className?: string
}

const columns: readonly TableColumn<PublicPlan>[] = [
  {
    key: 'name',
    header: '套餐',
    render: (plan) => <span className="font-medium text-ink">{plan.name}</span>,
  },
  {
    key: 'rpm',
    header: 'RPM',
    align: 'right',
    render: (plan) => <span className="tabular-nums">{formatInteger(plan.rpm)}</span>,
  },
  {
    key: 'tpm',
    header: 'TPM',
    align: 'right',
    render: (plan) => <span className="tabular-nums">{formatInteger(plan.tpm)}</span>,
  },
  {
    key: 'concurrency',
    header: '并发',
    align: 'right',
    render: (plan) => <span className="tabular-nums">{formatInteger(plan.concurrency)}</span>,
  },
  {
    key: 'daily',
    header: '日额度',
    align: 'right',
    render: (plan) => (
      <span className="tabular-nums">{formatQuotaCredits(plan.daily_quota_micro)}</span>
    ),
  },
  {
    key: 'price',
    header: '价格',
    align: 'right',
    render: (plan) => (
      <span className="font-medium tabular-nums text-ink">{formatPlanPrice(plan.price_fen)}</span>
    ),
  },
]

/**
 * F2-2: the four tiers live in one table with exactly six columns. The
 * FREE_DEFAULT rate policy is intentionally excluded.
 */
export function PlanLimitTable({
  plans,
  loading = false,
  error = null,
  onRetry,
  className,
}: PlanLimitTableProps) {
  const rows = useMemo(() => selectPurchasablePlans(plans), [plans])

  return (
    <Table
      className={className}
      ariaLabel="套餐限流对比"
      columns={columns}
      rows={rows}
      rowKey={(plan) => plan.code}
      loading={loading}
      error={error}
      onRetry={onRetry}
      emptyTitle="暂无可售套餐"
      emptyDescription="套餐配置尚未返回，请稍后重试。"
    />
  )
}
