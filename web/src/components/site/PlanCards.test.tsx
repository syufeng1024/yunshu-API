import { cleanup, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { fixtureResponses } from '@/fixtures/public'
import { PlanCards } from './PlanCards'

const plans = fixtureResponses['/api/public/plans'].data

function cardFor(name: string): HTMLElement {
  const card = screen
    .getAllByTestId('plan-card')
    .find((candidate) => within(candidate).queryByText(name) !== null)
  if (!card) throw new Error(`plan card not found: ${name}`)
  return card
}

describe('PlanCards', () => {
  afterEach(() => {
    cleanup()
  })

  it('F2-1: anonymous CTAs carry the plan marker into registration', () => {
    render(
      <MemoryRouter>
        <PlanCards plans={plans} authenticated={false} />
      </MemoryRouter>,
    )

    expect(screen.getAllByTestId('plan-card')).toHaveLength(4)
    expect(screen.queryByText('默认档')).not.toBeInTheDocument()
    expect(screen.queryByText(/订阅/)).not.toBeInTheDocument()
    expect(screen.queryByText(/周期/)).not.toBeInTheDocument()

    const href = within(cardFor('标准版')).getByRole('link', { name: '选择此套餐' }).getAttribute('href')
    expect(href).toContain('/login?')
    expect(href).toContain('plan=STANDARD')
    expect(href).toContain('tab=register')
  })

  it('F2-1: authenticated CTAs go to recharge with the plan preselected', () => {
    render(
      <MemoryRouter>
        <PlanCards plans={plans} authenticated />
      </MemoryRouter>,
    )

    const href = within(cardFor('专业版')).getByRole('link', { name: '选择此套餐' }).getAttribute('href')
    expect(href).toBe('/console/recharge?plan=PRO')
  })

  it('A25: invoice support follows the plan flags', () => {
    render(
      <MemoryRouter>
        <PlanCards plans={plans} authenticated={false} />
      </MemoryRouter>,
    )

    expect(within(cardFor('标准版')).getByText('支持')).toBeInTheDocument()
    expect(within(cardFor('专业版')).getByText('支持')).toBeInTheDocument()
    expect(within(cardFor('企业版')).getByText('支持')).toBeInTheDocument()
    expect(within(cardFor('体验版')).getByText('不支持')).toBeInTheDocument()
  })

  it('renders ErrorState instead of empty cards when the plan request fails', () => {
    render(
      <MemoryRouter>
        <PlanCards plans={[]} error="套餐配置加载失败" authenticated={false} />
      </MemoryRouter>,
    )
    expect(screen.getByRole('alert')).toHaveTextContent('套餐配置加载失败')
    expect(screen.queryByTestId('plan-card')).not.toBeInTheDocument()
  })
})
