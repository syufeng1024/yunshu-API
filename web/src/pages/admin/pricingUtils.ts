import type { AdminPriceRule } from '@/api/admin'

/**
 * Pure pricing helpers. Keeping the comparison and conflict logic out of JSX
 * makes the two rules the backend enforces easy to unit test:
 *   - a raise needs the seven-day notice window (pricing.go:19);
 *   - two enabled rules whose windows overlap must carry the same signature,
 *     otherwise they are a truth-source conflict (pricing.go:821-856).
 */

export type PriceDirection = 'raise' | 'cut'

export interface PriceSnapshot {
  in_price_micro_per_k: number
  out_price_micro_per_k: number
  per_call_price_micro: number
  long_ctx_in_price_micro_per_k?: number | null
  long_ctx_out_price_micro_per_k?: number | null
}

function increased(next: number | null | undefined, current: number | null | undefined): boolean {
  return typeof next === 'number' && typeof current === 'number' && next > current
}

/**
 * Mirrors isPriceIncrease (pricing.go:673-685): any customer-facing increase
 * is a raise, even when another dimension drops. A first-ever price has no
 * baseline and is not a raise — it may take effect immediately.
 */
export function resolvePriceDirection(
  current: PriceSnapshot | null,
  next: PriceSnapshot,
): PriceDirection {
  if (current === null) return 'cut'
  if (increased(next.in_price_micro_per_k, current.in_price_micro_per_k)) return 'raise'
  if (increased(next.out_price_micro_per_k, current.out_price_micro_per_k)) return 'raise'
  if (increased(next.per_call_price_micro, current.per_call_price_micro)) return 'raise'
  if (
    increased(
      next.long_ctx_in_price_micro_per_k,
      current.long_ctx_in_price_micro_per_k,
    )
  ) {
    return 'raise'
  }
  if (
    increased(
      next.long_ctx_out_price_micro_per_k,
      current.long_ctx_out_price_micro_per_k,
    )
  ) {
    return 'raise'
  }
  return 'cut'
}

function timestamp(value: string): number {
  return new Date(value).getTime()
}

function windowEnd(rule: AdminPriceRule): number {
  if (!rule.effective_to) return Number.POSITIVE_INFINITY
  const end = timestamp(rule.effective_to)
  return Number.isNaN(end) ? Number.POSITIVE_INFINITY : end
}

/** Half-open windows [from, to); the boundary instant is not an overlap. */
export function windowsOverlap(first: AdminPriceRule, second: AdminPriceRule): boolean {
  const firstFrom = timestamp(first.effective_from)
  const secondFrom = timestamp(second.effective_from)
  if (Number.isNaN(firstFrom) || Number.isNaN(secondFrom)) return false
  return firstFrom < windowEnd(second) && secondFrom < windowEnd(first)
}

function nullable(value: number | null | undefined): number | null {
  return value === null || value === undefined ? null : value
}

/** Same dimensions as priceRow.signatureFull (pricing.go:137-144). */
export function priceSignature(rule: AdminPriceRule): string {
  return JSON.stringify([
    rule.in_price_micro_per_k,
    rule.out_price_micro_per_k,
    rule.per_call_price_micro,
    nullable(rule.cache_hit_ratio_bp),
    nullable(rule.cache_write_ratio_bp),
    nullable(rule.long_ctx_threshold_tokens),
    nullable(rule.long_ctx_in_price_micro_per_k),
    nullable(rule.long_ctx_out_price_micro_per_k),
    nullable(rule.cost_in_micro_per_k),
    nullable(rule.cost_out_micro_per_k),
    nullable(rule.cost_per_call_micro),
  ])
}

export interface PriceConflictPair {
  first: AdminPriceRule
  second: AdminPriceRule
}

/** Only enabled rows participate, matching overlappingPriceRules (pricing.go:883). */
export function findPriceConflicts(rules: readonly AdminPriceRule[]): PriceConflictPair[] {
  const enabled = rules.filter((rule) => rule.enabled)
  const pairs: PriceConflictPair[] = []
  for (let i = 0; i < enabled.length; i += 1) {
    for (let j = i + 1; j < enabled.length; j += 1) {
      const first = enabled[i]
      const second = enabled[j]
      if (!windowsOverlap(first, second)) continue
      if (priceSignature(first) === priceSignature(second)) continue
      pairs.push({ first, second })
    }
  }
  return pairs
}

/** The rule effective at `now`, newest version first (pricing.go:902-910). */
export function activePriceRule(
  rules: readonly AdminPriceRule[],
  now = new Date(),
): AdminPriceRule | null {
  const at = now.getTime()
  const active = rules
    .filter((rule) => rule.enabled)
    .filter((rule) => {
      const from = timestamp(rule.effective_from)
      if (Number.isNaN(from) || from > at) return false
      return rule.effective_to === null || rule.effective_to === undefined
        ? true
        : timestamp(rule.effective_to) > at
    })
    .sort((a, b) => b.version - a.version || b.id - a.id)
  return active[0] ?? null
}
