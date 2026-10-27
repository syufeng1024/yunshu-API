import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { QuotaBar } from './QuotaBar'
import { EtaHint } from './EtaHint'

afterEach(cleanup)

const int = (value: number) => String(value)

describe('QuotaBar', () => {
  it('renders "无上限" for a null limit instead of a zero ratio', () => {
    render(
      <QuotaBar
        dimensions={[
          {
            key: 'daily',
            label: '日额度',
            used: 1_000_000,
            limit: null,
            unlimitedLabel: '无上限',
            format: int,
          },
        ]}
      />,
    )
    expect(screen.getByText('无上限')).toBeInTheDocument()
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
  })

  it('marks a dimension without live usage as unavailable rather than 0', () => {
    render(
      <QuotaBar
        dimensions={[{ key: 'rpm', label: 'RPM', used: null, limit: 60, format: int }]}
      />,
    )
    expect(screen.getByText('实时使用量不可用')).toBeInTheDocument()
    expect(screen.queryByText('0%')).not.toBeInTheDocument()
  })

  it('renders used/limit and the percentage for a measured dimension', () => {
    render(
      <QuotaBar dimensions={[{ key: 'rpm', label: 'RPM', used: 30, limit: 60, format: int }]} />,
    )
    expect(screen.getByText(/30/)).toBeInTheDocument()
    expect(screen.getByText(/60/)).toBeInTheDocument()
    expect(screen.getByText('50%')).toBeInTheDocument()
  })
})

describe('EtaHint', () => {
  it('shows 无日额度上限 for NO_DAILY_QUOTA instead of 0 or a blank', () => {
    render(
      <EtaHint
        eta={{
          reachable: false,
          reason: 'NO_DAILY_QUOTA',
          eta_at: null,
          seconds_left: null,
          burn_micro_per_hour: 0,
        }}
      />,
    )
    expect(screen.getByText('无日额度上限')).toBeInTheDocument()
    expect(screen.queryByText(/0/)).not.toBeInTheDocument()
  })

  it('reports the reached state distinctly', () => {
    render(
      <EtaHint
        eta={{
          reachable: true,
          reason: 'QUOTA_REACHED',
          eta_at: '2026-10-27T10:00:00Z',
          seconds_left: 0,
          burn_micro_per_hour: 1000,
        }}
      />,
    )
    expect(screen.getByText('今日额度已触顶')).toBeInTheDocument()
  })

  it('renders the extrapolated time for OK', () => {
    render(
      <EtaHint
        eta={{
          reachable: true,
          reason: 'OK',
          eta_at: '2026-10-27T10:00:00Z',
          seconds_left: 5400,
          burn_micro_per_hour: 2_000_000,
        }}
      />,
    )
    expect(screen.getByText(/后触顶/)).toBeInTheDocument()
    expect(screen.getByText(/1 小时 30 分钟/)).toBeInTheDocument()
  })
})
