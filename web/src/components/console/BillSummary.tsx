import type { BillResponse } from '@/api/console'
import { ErrorState, StatCard } from '@/components'
import {
  formatBasisPoints,
  formatCreditAmount,
  formatInteger,
} from '@/lib/format'

export interface BillSummaryProps {
  bill: BillResponse | undefined
  loading: boolean
  error: string | null
  hint?: string | null
  onRetry: () => void
}

/**
 * Monthly bill. Both money representations come straight from the response:
 * amount_yuan is already "123.45" and consumed_micro becomes 12,345 through
 * formatCreditAmount. This component must not derive one from the other
 * (F9-2 / hard constraint 16).
 */
export function BillSummary({ bill, loading, error, hint, onRetry }: BillSummaryProps) {
  if (error) {
    return <ErrorState message={error} hint={hint} onRetry={onRetry} className="bg-surface" />
  }

  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="账单金额"
          loading={loading}
          value={bill ? `${bill.amount_yuan} 元` : undefined}
          hint="服务端按分四舍五入后给出两位小数"
        />
        <StatCard
          label="消耗积分"
          loading={loading}
          value={bill ? `${formatCreditAmount(bill.consumed_micro)} 积分` : undefined}
          hint="仅统计成功请求"
        />
        <StatCard
          label="充值积分消耗"
          loading={loading}
          value={
            bill ? `${formatCreditAmount(bill.consumed_recharge_micro)} 积分` : undefined
          }
        />
        <StatCard
          label="赠送积分消耗"
          loading={loading}
          value={bill ? `${formatCreditAmount(bill.consumed_bonus_micro)} 积分` : undefined}
        />
      </div>

      <div className="grid gap-4 rounded-xl border border-border bg-surface p-4 text-sm shadow-card sm:grid-cols-3">
        <div>
          <p className="text-xs text-ink-secondary">请求数（含失败）</p>
          <p className="mt-1 font-medium tabular-nums text-ink">
            {bill ? formatInteger(bill.request_total) : '—'}
          </p>
        </div>
        <div>
          <p className="text-xs text-ink-secondary">成功请求</p>
          <p className="mt-1 font-medium tabular-nums text-ink">
            {bill ? formatInteger(bill.request_success) : '—'}
          </p>
        </div>
        <div>
          <p className="text-xs text-ink-secondary">成功率</p>
          <p className="mt-1 font-medium tabular-nums text-ink">
            {bill ? formatBasisPoints(bill.success_rate_bp) : '—'}
          </p>
        </div>
      </div>
    </div>
  )
}
