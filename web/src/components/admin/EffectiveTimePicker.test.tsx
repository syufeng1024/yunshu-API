import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { EffectiveTimePicker, toLocalDateTimeValue } from './EffectiveTimePicker'

afterEach(cleanup)

// Local wall-clock time: today + 7 days is 2026-11-04 in the same zone.
const NOW = new Date(2026, 9, 28, 12, 30, 0)

function expectedDate(base: Date, days: number): string {
  const date = new Date(base.getTime())
  date.setDate(date.getDate() + days)
  return toLocalDateTimeValue(date).slice(0, 10)
}

describe('EffectiveTimePicker', () => {
  it('EffectiveTimePicker：涨价最小可选 today+7', () => {
    const raise = render(
      <EffectiveTimePicker direction="raise" value="" onChange={() => undefined} now={NOW} />,
    )
    const raiseInput = screen.getByTestId('effective-time-input') as HTMLInputElement
    expect(raiseInput.min).toBe(`${expectedDate(NOW, 7)}T12:30`)
    raise.unmount()

    render(
      <EffectiveTimePicker direction="cut" value="" onChange={() => undefined} now={NOW} />,
    )
    const cutInput = screen.getByTestId('effective-time-input') as HTMLInputElement
    expect(cutInput.min).toBe(`${expectedDate(NOW, 0)}T12:30`)
    expect(cutInput.min.startsWith('2026-10-28')).toBe(true)
  })
})
