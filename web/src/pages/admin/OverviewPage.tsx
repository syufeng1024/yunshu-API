import { errorHint, errorMessage } from '@/api/client'
import { useAdminOverview } from '@/api/admin'
import { Badge, ErrorState, PageHeader, Skeleton, StatCard } from '@/components'
import {
  formatBasisPoints,
  formatCreditAmount,
  formatInteger,
} from '@/lib/format'
import { usePageTitle } from '@/lib/usePageTitle'

function AlertCard({
  label,
  value,
  description,
}: {
  label: string
  value: number
  description: string
}) {
  const active = value > 0
  return (
    <div className="rounded-xl border border-border bg-surface p-5 shadow-card">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-ink">{label}</h3>
        <Badge variant={active ? 'danger' : 'success'} dot>
          {active ? '需关注' : '正常'}
        </Badge>
      </div>
      <p className="mt-2 text-2xl font-semibold tabular-nums tracking-tight text-ink">
        {formatInteger(value)}
      </p>
      <p className="mt-1 text-xs text-ink-muted">{description}</p>
    </div>
  )
}

/** PRD §6.1. Every number comes from GET /api/admin/overview. */
export default function AdminOverviewPage() {
  usePageTitle('运营概览')
  const overview = useAdminOverview()

  const error = overview.isError ? errorMessage(overview.error) : null
  const hint = overview.isError ? errorHint(overview.error) : null
  const data = overview.data
  const loading = overview.isLoading

  const allAlertsClear =
    data !== undefined &&
    data.alerts.dangling_prehold === 0 &&
    data.alerts.recon_diff === 0 &&
    data.alerts.sla_overdue === 0 &&
    data.alerts.channel_abnormal === 0

  return (
    <div className="space-y-6">
      <PageHeader
        title="运营概览"
        description="今日为 UTC+8 自然日；成功率分母为 0 时显示 —，不以 0 代替。"
      />

      {error ? (
        <ErrorState
          message={error}
          hint={hint}
          onRetry={() => void overview.refetch()}
        />
      ) : (
        <>
          <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" aria-label="核心指标">
            <StatCard
              label="今日调用量"
              loading={loading}
              value={data ? formatInteger(data.today.requests) : undefined}
              hint="今日 t_usage_record 全部请求行"
            />
            <StatCard
              label="今日消耗"
              loading={loading}
              value={data ? `${formatCreditAmount(data.today.consumed_micro)} 积分` : undefined}
              hint="按今日全部请求汇总；失败请求消耗为 0"
            />
            <StatCard
              label="今日成功率"
              loading={loading}
              value={
                data
                  ? data.today.success_rate_bp === null
                    ? '—'
                    : formatBasisPoints(data.today.success_rate_bp)
                  : undefined
              }
              hint="SUCCESS / 非 402/429 请求；分母为 0 时为 —"
            />
            <StatCard
              label="待处理单据"
              loading={loading}
              value={data ? formatInteger(data.counts.pending_reviews) : undefined}
              hint="审核中心 PENDING 队列"
            />
            <StatCard
              label="未关闭工单"
              loading={loading}
              value={data ? formatInteger(data.counts.open_tickets) : undefined}
              hint="OPEN + RESPONDED；SLA 只提示不处置"
            />
            <StatCard
              label="异常渠道"
              loading={loading}
              value={data ? formatInteger(data.alerts.channel_abnormal) : undefined}
              hint="境内、启用且 health_state 非 CLOSED"
            />
          </section>

          <section className="space-y-3" aria-label="告警区">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="text-base font-semibold text-ink">告警区</h2>
              {allAlertsClear ? (
                <span className="text-xs text-success">当前四类告警均为 0</span>
              ) : null}
            </div>
            {loading ? (
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                {Array.from({ length: 4 }, (_, index) => (
                  <div
                    key={index}
                    className="space-y-3 rounded-xl border border-border bg-surface p-5 shadow-card"
                  >
                    <Skeleton className="h-4 w-24" />
                    <Skeleton className="h-8 w-16" />
                    <Skeleton className="h-3 w-full" />
                  </div>
                ))}
              </div>
            ) : (
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                <AlertCard
                  label="悬挂预扣"
                  value={data?.alerts.dangling_prehold ?? 0}
                  description="t_prehold_hold.state=HELD 且已过 expires_at"
                />
                <AlertCard
                  label="对账差异"
                  value={data?.alerts.recon_diff ?? 0}
                  description="近 30 天 status=OPEN 的 t_reconcile_diff"
                />
                <AlertCard
                  label="SLA 超时工单"
                  value={data?.alerts.sla_overdue ?? 0}
                  description="OPEN、未响应且已过首次响应时限；保持 OPEN，需人工处理"
                />
                <AlertCard
                  label="异常渠道"
                  value={data?.alerts.channel_abnormal ?? 0}
                  description="境内、启用、未删除且 health_state 非 CLOSED"
                />
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}
