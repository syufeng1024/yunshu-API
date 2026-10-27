import { describe, expect, it } from 'vitest'
import {
  creditsFromMicro,
  fenFromYuanInput,
  formatCreditsFromMicro,
  formatPercent,
  formatPlanPrice,
  formatPricePerK,
  formatQuotaCredits,
  formatTokens,
  formatYuanFromCredits,
  formatYuanFromFen,
  yuanFromCredits,
} from './format'

describe('credit conversions', () => {
  it('12345 credits -> 123.45 yuan', () => {
    expect(yuanFromCredits(12345)).toBe(123.45)
    expect(formatYuanFromCredits(12345)).toBe('123.45')
  })

  it('1500 micro-credits per 1k tokens -> 0.15 credits', () => {
    expect(creditsFromMicro(1500)).toBe(0.15)
    expect(formatCreditsFromMicro(1500)).toBe('0.15')
    expect(formatPricePerK(1500)).toBe('0.15 积分/千 token')
  })

  it('keeps two decimals for yuan and trims credit zeros', () => {
    expect(formatYuanFromCredits(0)).toBe('0.00')
    expect(formatYuanFromCredits(-12345)).toBe('-123.45')
    expect(formatCreditsFromMicro(12345678)).toBe('1234.5678')
    expect(formatCreditsFromMicro(0)).toBe('0')
  })

  it('returns the neutral placeholder for non-finite input', () => {
    expect(formatYuanFromCredits(Number.NaN)).toBe('—')
    expect(formatCreditsFromMicro(Number.POSITIVE_INFINITY)).toBe('—')
  })

  it('formats ratios as percentages', () => {
    expect(formatPercent(0.152)).toBe('15.2%')
  })

  it('formats fen, plan prices, quotas and token counts', () => {
    expect(formatYuanFromFen(9900)).toBe('99.00')
    expect(formatPlanPrice(9900)).toBe('99 元')
    expect(formatPlanPrice(0)).toBe('免费')
    expect(formatQuotaCredits(50000000)).toBe('5,000 积分')
    expect(formatQuotaCredits(null)).toBe('无上限')
    expect(formatTokens(32768)).toBe('32,768 tokens')
  })

  it('normalises a user-entered yuan amount to fen', () => {
    expect(fenFromYuanInput('99')).toBe(9900)
    expect(fenFromYuanInput('99.5')).toBe(9950)
    expect(fenFromYuanInput(' ￥1,000.00 ')).toBe(100_000)
    expect(fenFromYuanInput('９９.５')).toBe(9950)
    expect(fenFromYuanInput('0')).toBeNull()
    expect(fenFromYuanInput('1.234')).toBeNull()
    expect(fenFromYuanInput('-5')).toBeNull()
    expect(fenFromYuanInput('abc')).toBeNull()
  })
})
