import { formatBasisPoints, formatInteger } from '@/lib/format'

/**
 * Gross-margin floor, transcribed from ops/pricing.go:
 *
 *   3 * P * ratioBP >= 40000 * C      (integer comparison only)
 *   required = ceil(4 * C / 3)        = (40000*C + 29999) / 30000
 *   shortfall = required - actual,    actual = P × ratioBP ÷ GROUP_RATIO_SCALE
 *
 * A float would lose a credit on large costs, so the verdict uses integers.
 * The margin figure is display-only.
 */
const FLOOR_CROSS_FACTOR = 40_000
const FLOOR_SCALE = 30_000
const GROUP_RATIO_SCALE = 10_000
const MARGIN_FLOOR_BP = 2_500

export interface FloorCheckHintProps {
  /** Standard price in micro-credits per 1k tokens (or per call for IMAGE). */
  priceMicroPerK: number
  /** Group multiplier in basis points; 0 means internal (not billed). */
  groupRatioBp: number
  /** Upstream cost from t_price_rule; null means "not configured". */
  costMicroPerK: number | null
  /** Unit copy appended to the shortfall figure. */
  unit?: string
}

export function FloorCheckHint({
  priceMicroPerK,
  groupRatioBp,
  costMicroPerK,
  unit = '积分/千 token',
}: FloorCheckHintProps) {
  if (costMicroPerK === null) {
    return (
      <p
        data-testid="floor-check-hint"
        className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-xs text-ink-secondary"
      >
        <span data-testid="floor-check-no-cost">无成本价，不做地板校验</span>
        ，保存需显式确认。
      </p>
    )
  }

  // internal (ratio 0) does not participate in the floor at all — the 0 is
  // "not billed", never a free discount (T15 hard constraint 5).
  if (groupRatioBp <= 0) {
    return (
      <p
        data-testid="floor-check-hint"
        className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-xs text-ink-secondary"
      >
        <span data-testid="floor-check-internal">该分组倍率为 0（internal），语义为不计费，不参与毛利地板校验。</span>
      </p>
    )
  }

  if (!Number.isFinite(priceMicroPerK) || !Number.isFinite(costMicroPerK)) {
    return null
  }

  const actual = Math.floor((priceMicroPerK * groupRatioBp) / GROUP_RATIO_SCALE)
  const required = Math.floor(
    (FLOOR_CROSS_FACTOR * costMicroPerK + FLOOR_SCALE - 1) / FLOOR_SCALE,
  )
  const meets = 3 * priceMicroPerK * groupRatioBp >= FLOOR_CROSS_FACTOR * costMicroPerK

  if (meets) {
    return (
      <p
        data-testid="floor-check-hint"
        className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-xs text-ink-secondary"
      >
        <span data-testid="floor-check-pass">毛利地板校验通过</span>
        ，折后标准单价 {formatInteger(actual)} {unit}，不低于地板要求。
      </p>
    )
  }

  const shortfall = required - actual
  const marginBp =
    actual > 0 ? Math.floor(((actual - costMicroPerK) * 10_000) / actual) : null
  const marginText =
    marginBp === null ? '当前毛利率无法计算' : `当前毛利率 ${formatBasisPoints(marginBp)}`

  return (
    <div
      role="alert"
      data-testid="floor-check-hint"
      className="space-y-1 rounded-lg border border-warning bg-warning-soft px-3 py-2 text-xs text-warning"
    >
      <p>
        {marginText}，地板 {formatBasisPoints(MARGIN_FLOOR_BP, 0)}，
        <span data-testid="floor-check-difference">
          差额 {formatInteger(shortfall)} {unit}
        </span>
        。
      </p>
      <p className="text-ink-secondary">
        折后标准单价 {formatInteger(actual)} {unit}，低于成本 {formatInteger(costMicroPerK)} × 4/3 后取整的要求。
      </p>
    </div>
  )
}
