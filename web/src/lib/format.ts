/**
 * Single conversion point for credits <-> yuan and micro-credits <-> credits.
 *
 * PRD §3.1: 1 yuan = 100 credits; 1 credit = 10,000 micro-credits.
 * Pages must call these helpers instead of writing a second `/ 100` or
 * `/ 10000`. Amounts are rounded at the requested display precision only.
 */

export const MICRO_PER_CREDIT = 10_000
export const CREDITS_PER_YUAN = 100
export const FEN_PER_YUAN = 100

const INVALID = '—'

function roundHalfAwayFromZero(value: number): number {
  return value < 0 ? -Math.round(-value) : Math.round(value)
}

function trimFraction(value: number, fractionDigits: number): string {
  if (!Number.isFinite(value)) return INVALID
  return value.toFixed(fractionDigits).replace(/\.?0+$/, '')
}

/** Credits -> yuan, rounded to 2 decimal places. 12345 -> 123.45 */
export function yuanFromCredits(credits: number): number {
  if (!Number.isFinite(credits)) return Number.NaN
  return roundHalfAwayFromZero(credits) / CREDITS_PER_YUAN
}

/** Credits -> yuan display string with exactly 2 decimals. */
export function formatYuanFromCredits(credits: number): string {
  const yuan = yuanFromCredits(credits)
  return Number.isFinite(yuan) ? yuan.toFixed(2) : INVALID
}

/** Fen -> yuan, rounded to 2 decimal places. 9900 -> "99.00". */
export function formatYuanFromFen(fen: number): string {
  if (!Number.isFinite(fen)) return INVALID
  return (roundHalfAwayFromZero(fen) / FEN_PER_YUAN).toFixed(2)
}

/** Plan price: 0 -> "免费", otherwise "<yuan> 元" with trailing zeros trimmed. */
export function formatPlanPrice(fen: number): string {
  if (!Number.isFinite(fen)) return INVALID
  return fen === 0 ? '免费' : `${trimFraction(fen / FEN_PER_YUAN, 2)} 元`
}

/** Integer credits get zh-CN grouping; fractional values keep up to 4 decimals. */
export function formatCreditAmount(micro: number): string {
  const credits = creditsFromMicro(micro)
  if (!Number.isFinite(credits)) return INVALID
  return Number.isInteger(credits) ? formatInteger(credits) : formatCredits(credits, 4)
}

/** Daily quota in micro-credits; null means the plan has no daily cap. */
export function formatQuotaCredits(micro: number | null): string {
  if (micro === null) return '无上限'
  return `${formatCreditAmount(micro)} 积分`
}

/** Context window in tokens. */
export function formatTokens(tokens: number): string {
  return Number.isFinite(tokens) ? `${formatInteger(tokens)} tokens` : INVALID
}

/** Micro-credits -> credits, rounded to at most 4 decimal places. */
export function creditsFromMicro(micro: number): number {
  if (!Number.isFinite(micro)) return Number.NaN
  return roundHalfAwayFromZero(micro) / MICRO_PER_CREDIT
}

/** Micro-credits -> credits display string, trailing zeros trimmed. */
export function formatCreditsFromMicro(micro: number): string {
  return trimFraction(creditsFromMicro(micro), 4)
}

/** Credits display, trailing zeros trimmed. */
export function formatCredits(credits: number, fractionDigits = 2): string {
  return trimFraction(credits, fractionDigits)
}

/** Unit price: micro-credits per 1k tokens -> "0.15 积分/千 token". */
export function formatPricePerK(microPerK: number): string {
  const value = formatCreditsFromMicro(microPerK)
  return value === INVALID ? INVALID : `${value} 积分/千 token`
}

/** Integer with zh-CN grouping. */
export function formatInteger(value: number): string {
  return Number.isFinite(value) ? new Intl.NumberFormat('zh-CN').format(value) : INVALID
}

/** Credits -> micro-credits, the inverse of creditsFromMicro. */
export function microFromCredits(credits: number): number {
  if (!Number.isFinite(credits)) return Number.NaN
  return roundHalfAwayFromZero(credits) * MICRO_PER_CREDIT
}

/**
 * Basis points -> percent. 9850 -> "98.5%". Used for success_rate_bp and other
 * integers the backend reports in 1/10000 units.
 */
export function formatBasisPoints(bp: number, fractionDigits = 1): string {
  if (!Number.isFinite(bp)) return INVALID
  return formatPercent(bp / 10_000, fractionDigits)
}

/** Basis points -> price multiplier. 12000 -> "1.20". Not a fee estimate. */
export function formatRatioBp(bp: number, fractionDigits = 2): string {
  if (!Number.isFinite(bp)) return INVALID
  return (bp / 10_000).toFixed(fractionDigits)
}

/** Ratio in [0, 1] -> percent string. 0.152 -> "15.2%". */
export function formatPercent(ratio: number, fractionDigits = 1): string {
  if (!Number.isFinite(ratio)) return INVALID
  return new Intl.NumberFormat('zh-CN', {
    style: 'percent',
    minimumFractionDigits: 0,
    maximumFractionDigits: fractionDigits,
  }).format(ratio)
}

/** Seconds -> coarse duration copy for predictions ("1 小时 30 分钟"). */
export function formatDurationSeconds(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return INVALID
  const totalMinutes = Math.floor(seconds / 60)
  if (totalMinutes < 1) return `${Math.max(1, Math.round(seconds))} 秒`
  const hours = Math.floor(totalMinutes / 60)
  const minutes = totalMinutes % 60
  if (hours === 0) return `${minutes} 分钟`
  if (minutes === 0) return `${hours} 小时`
  return `${hours} 小时 ${minutes} 分钟`
}

/** Milliseconds -> compact duration. */
export function formatDurationMs(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return INVALID
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  return `${Math.floor(ms / 60_000)} min ${Math.round((ms % 60_000) / 1000)} s`
}

/** ISO timestamp -> local date-time, or the neutral placeholder. */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return INVALID
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return INVALID
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
}
