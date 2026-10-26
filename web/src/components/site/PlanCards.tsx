import { Link } from 'react-router-dom'
import type { PublicPlan } from '@/api/public'
import { Badge, EmptyState, ErrorState, Skeleton, buttonVariants } from '@/components'
import { formatCreditAmount, formatInteger, formatPlanPrice, formatQuotaCredits } from '@/lib/format'
import { cn } from '@/lib/cn'
import {
  invoiceLabel,
  planCtaPath,
  planScopeLabel,
  selectPurchasablePlans,
  slaLabel,
} from '@/lib/site'

export interface PlanCardsProps {
  plans: readonly PublicPlan[]
  loading?: boolean
  error?: string | null
  onRetry?: () => void
  authenticated: boolean
}

function LimitItem({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg bg-surface-muted px-3 py-2">
      <dt className="text-xs text-ink-muted">{label}</dt>
      <dd className="mt-0.5 whitespace-nowrap text-sm font-medium tabular-nums text-ink">{value}</dd>
    </div>
  )
}

/**
 * F2-1: plan CTA routes anonymous visitors to registration with the plan code
 * and authenticated users to the recharge page with the same preselect marker.
 */
export function PlanCards({
  plans,
  loading = false,
  error = null,
  onRetry,
  authenticated,
}: PlanCardsProps) {
  const rows = selectPurchasablePlans(plans)

  if (loading) {
    return (
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        {Array.from({ length: 4 }, (_, index) => (
          <Skeleton key={index} className="h-[26rem] w-full" />
        ))}
      </div>
    )
  }

  if (error) {
    return <ErrorState message={error} onRetry={onRetry} />
  }

  if (rows.length === 0) {
    return <EmptyState title="暂无可售套餐" description="套餐配置尚未返回，请稍后重试。" />
  }

  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
      {rows.map((plan) => (
        <article
          key={plan.code}
          data-testid="plan-card"
          className={cn(
            'flex flex-col gap-5 rounded-xl border bg-surface p-5 shadow-card',
            plan.code === 'STANDARD' ? 'border-primary ring-1 ring-primary-ring' : 'border-border',
          )}
        >
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-base font-semibold text-ink">{plan.name}</h3>
              {plan.code === 'STANDARD' ? <Badge variant="primary">常用</Badge> : null}
            </div>
            <p className="text-2xl font-semibold tracking-tight text-ink">
              {formatPlanPrice(plan.price_fen)}
            </p>
            <p className="text-xs text-ink-muted">
              {plan.price_fen === 0 ? '体验权益' : '一次性购买'}
            </p>
          </div>

          <div className="space-y-1 text-sm">
            <p className="text-ink">
              赠送 <span className="font-medium tabular-nums">{formatCreditAmount(plan.grant_bonus_micro)}</span> 积分
            </p>
            {plan.grant_recharge_micro > 0 ? (
              <p className="text-xs text-ink-secondary">
                充值 <span className="tabular-nums">{formatCreditAmount(plan.grant_recharge_micro)}</span> 积分
              </p>
            ) : null}
          </div>

          <dl className="grid grid-cols-2 gap-2">
            <LimitItem label="RPM" value={formatInteger(plan.rpm)} />
            <LimitItem label="TPM" value={formatInteger(plan.tpm)} />
            <LimitItem label="并发" value={formatInteger(plan.concurrency)} />
            <LimitItem label="日额度" value={formatQuotaCredits(plan.daily_quota_micro)} />
          </dl>

          <dl className="space-y-2 text-xs">
            <div className="flex items-start justify-between gap-3">
              <dt className="text-ink-muted">可调用模型</dt>
              <dd className="text-right text-ink-secondary">{planScopeLabel(plan.model_scope_mode)}</dd>
            </div>
            <div className="flex items-start justify-between gap-3">
              <dt className="text-ink-muted">发票</dt>
              <dd className="text-right text-ink-secondary">{invoiceLabel(plan)}</dd>
            </div>
            <div className="flex items-start justify-between gap-3">
              <dt className="text-ink-muted">SLA</dt>
              <dd className="text-right text-ink-secondary">{slaLabel(plan)}</dd>
            </div>
          </dl>

          <Link
            to={planCtaPath(plan.code, authenticated)}
            className={cn(
              buttonVariants({ variant: plan.code === 'STANDARD' ? 'primary' : 'secondary', size: 'md' }),
              'mt-auto w-full',
            )}
          >
            {plan.code === 'TRIAL' ? '开始体验' : '选择此套餐'}
          </Link>
        </article>
      ))}
    </div>
  )
}
