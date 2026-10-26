import { describe, expect, it } from 'vitest'
import {
  creditsFromMicro,
  formatCreditsFromMicro,
  formatPercent,
  formatPricePerK,
  formatYuanFromCredits,
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
})
