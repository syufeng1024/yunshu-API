import { describe, expect, it } from 'vitest'
import type { AdminPriceRule } from '@/api/admin'
import { findPriceConflicts, resolvePriceDirection } from './pricingUtils'

function rule(overrides: Partial<AdminPriceRule> = {}): AdminPriceRule {
  return {
    id: 1,
    scope_level: 'MODEL',
    model_id: 11,
    model_name: 'qwen-max',
    model_type: 'TEXT',
    in_price_micro_per_k: 1000,
    out_price_micro_per_k: 3000,
    per_call_price_micro: 0,
    cost_in_micro_per_k: 800,
    cost_out_micro_per_k: 2400,
    currency_cost: 'CNY',
    version: 3,
    effective_from: '2026-10-01T00:00:00Z',
    effective_to: null,
    enabled: true,
    created_at: '2026-09-20T02:00:00Z',
    ...overrides,
  }
}

describe('resolvePriceDirection', () => {
  const current = {
    in_price_micro_per_k: 1000,
    out_price_micro_per_k: 3000,
    per_call_price_micro: 0,
  }

  it('returns raise when any customer-facing price increases', () => {
    expect(
      resolvePriceDirection(current, { ...current, in_price_micro_per_k: 1200 }),
    ).toBe('raise')
    expect(
      resolvePriceDirection(current, { ...current, out_price_micro_per_k: 3600 }),
    ).toBe('raise')
  })

  it('returns cut for a decrease or an unchanged price', () => {
    expect(
      resolvePriceDirection(current, { ...current, in_price_micro_per_k: 900 }),
    ).toBe('cut')
    expect(resolvePriceDirection(current, current)).toBe('cut')
  })

  it('treats a first-ever price as immediate, not a raise', () => {
    expect(resolvePriceDirection(null, current)).toBe('cut')
  })
})

describe('findPriceConflicts', () => {
  it('reports two overlapping enabled rules with different prices', () => {
    const pairs = findPriceConflicts([
      rule({ id: 1 }),
      rule({ id: 2, in_price_micro_per_k: 1200, version: 4 }),
    ])
    expect(pairs).toHaveLength(1)
    expect([pairs[0].first.id, pairs[0].second.id].sort()).toEqual([1, 2])
  })

  it('ignores adjacent windows and identical signatures', () => {
    expect(
      findPriceConflicts([
        rule({ id: 1, effective_to: '2026-11-05T00:00:00Z' }),
        rule({ id: 2, effective_from: '2026-11-05T00:00:00Z' }),
      ]),
    ).toHaveLength(0)

    expect(
      findPriceConflicts([
        rule({ id: 1 }),
        rule({ id: 2, in_price_micro_per_k: 1000 }),
      ]),
    ).toHaveLength(0)
  })

  it('ignores disabled rows', () => {
    expect(
      findPriceConflicts([
        rule({ id: 1 }),
        rule({ id: 2, in_price_micro_per_k: 1200, enabled: false }),
      ]),
    ).toHaveLength(0)
  })
})
