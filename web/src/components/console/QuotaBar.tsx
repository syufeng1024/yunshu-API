import { cn } from '@/lib/cn'
import { formatPercent } from '@/lib/format'

export interface QuotaDimension {
  key: string
  label: string
  /** null = the API has no live usage for this dimension; render it as unknown. */
  used: number | null
  /** null = no ceiling on the plan (render unlimitedLabel). */
  limit: number | null
  /** Display formatter for used/limit, e.g. credits or raw integers. */
  format: (value: number) => string
  unlimitedLabel?: string
  /** Extra note shown under the row, e.g. "账户维度". */
  note?: string
}

export interface QuotaBarProps {
  dimensions: readonly QuotaDimension[]
  title?: string
  description?: string
  className?: string
}

function toneFor(ratio: number): string {
  if (ratio >= 0.9) return 'bg-danger'
  if (ratio >= 0.75) return 'bg-warning'
  return 'bg-primary'
}

/**
 * Four-dimension quota strip (RPM / TPM / concurrency / daily credits).
 *
 * A dimension whose live usage is unavailable renders "实时使用量不可用" with
 * an empty track rather than 0%, and a plan with no daily ceiling renders
 * "无上限" rather than 0. Both are PRD hard constraint 6: never fake a value.
 */
export function QuotaBar({ dimensions, title = '账户额度', description, className }: QuotaBarProps) {
  return (
    <section
      aria-label={title}
      className={cn('rounded-xl border border-border bg-surface p-5 shadow-card', className)}
    >
      <div className="space-y-1">
        <h2 className="text-sm font-semibold text-ink">{title}</h2>
        {description ? <p className="text-xs text-ink-secondary">{description}</p> : null}
      </div>

      <dl className="mt-4 space-y-4">
        {dimensions.map((dimension) => {
          const unlimited = dimension.limit === null
          const unknown = !unlimited && dimension.used === null
          const ratio =
            !unlimited && !unknown && dimension.limit! > 0
              ? Math.min(1, Math.max(0, dimension.used! / dimension.limit!))
              : 0
          const percent = Math.round(ratio * 100)

          return (
            <div key={dimension.key} className="space-y-1.5">
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <dt className="text-xs font-medium text-ink-secondary">{dimension.label}</dt>
                <dd className="text-xs text-ink">
                  {unlimited ? (
                    <span className="text-ink-muted">{dimension.unlimitedLabel ?? '无上限'}</span>
                  ) : unknown ? (
                    <span className="text-ink-muted">实时使用量不可用</span>
                  ) : (
                    <span>
                      {dimension.format(dimension.used!)}
                      <span className="text-ink-muted"> / {dimension.format(dimension.limit!)}</span>
                      <span className="ml-2 text-ink-muted">{formatPercent(ratio)}</span>
                    </span>
                  )}
                </dd>
              </div>

              <div
                role="progressbar"
                aria-label={dimension.label}
                aria-valuemin={0}
                aria-valuemax={unlimited ? undefined : 100}
                aria-valuenow={unlimited || unknown ? undefined : percent}
                aria-valuetext={
                  unlimited
                    ? (dimension.unlimitedLabel ?? '无上限')
                    : unknown
                      ? '实时使用量不可用'
                      : `${percent}%`
                }
                className="h-2 w-full overflow-hidden rounded-full bg-surface-muted"
              >
                {!unlimited && !unknown ? (
                  <div
                    className={cn('h-full rounded-full transition-[width] duration-fast', toneFor(ratio))}
                    style={{ width: `${percent}%` }}
                  />
                ) : null}
              </div>

              {dimension.note ? <p className="text-xs text-ink-muted">{dimension.note}</p> : null}
            </div>
          )
        })}
      </dl>
    </section>
  )
}
