import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { fixtureResponses } from '@/fixtures/public'
import { PlanLimitTable } from './PlanLimitTable'

const plans = fixtureResponses['/api/public/plans'].data

describe('PlanLimitTable', () => {
  it('F2-2: renders the four tiers in one table with exactly six columns', () => {
    render(<PlanLimitTable plans={plans} />)

    for (const header of ['套餐', 'RPM', 'TPM', '并发', '日额度', '价格']) {
      expect(screen.getByRole('columnheader', { name: header })).toBeInTheDocument()
    }
    expect(screen.getAllByRole('columnheader')).toHaveLength(6)

    for (const name of ['体验版', '标准版', '专业版', '企业版']) {
      expect(screen.getByText(name)).toBeInTheDocument()
    }
    expect(screen.queryByText('默认档')).not.toBeInTheDocument()
  })

  it('renders ErrorState when the plan request fails', () => {
    render(<PlanLimitTable plans={[]} error="套餐配置加载失败" />)
    expect(screen.getByRole('alert')).toHaveTextContent('套餐配置加载失败')
    expect(screen.queryByText('暂无可售套餐')).not.toBeInTheDocument()
  })

  it('F1-3: renders a changed RPM value from the plan payload', () => {
    render(<PlanLimitTable plans={[{ ...plans[1], rpm: 299 }]} />)
    expect(screen.getByText('299')).toBeInTheDocument()
    expect(screen.queryByText('300')).not.toBeInTheDocument()
  })
})
