import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { usePublicPlans } from '@/api/public'
import { fixtureResponses } from '@/fixtures/public'
import { useAuthStore } from '@/stores/auth'
import LoginPage from './LoginPage'

vi.mock('@/api/public', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/public')>()
  return { ...actual, usePublicPlans: vi.fn() }
})

const plans = fixtureResponses['/api/public/plans'].data
const mockedPlans = vi.mocked(usePublicPlans)

beforeEach(() => {
  useAuthStore.setState({ status: 'anonymous', user: null, error: null, hint: null })
  mockedPlans.mockReturnValue({
    data: { data: plans },
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePublicPlans>)
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('LoginPage', () => {
  it('A22: uses active-claim wording and never says registration grants cash', () => {
    render(
      <MemoryRouter initialEntries={['/login?tab=register']}>
        <LoginPage />
      </MemoryRouter>,
    )

    expect(screen.getByRole('button', { name: '主动领取体验积分' })).toBeInTheDocument()
    expect(screen.getByText(/注册不会自动发放/)).toBeInTheDocument()
    expect(screen.queryByText(/注册即送/)).not.toBeInTheDocument()
  })

  it('renders the agreement checkbox and the plan marker from the query', () => {
    render(
      <MemoryRouter initialEntries={['/login?tab=register&plan=STANDARD']}>
        <LoginPage />
      </MemoryRouter>,
    )
    expect(screen.getByRole('checkbox')).toBeInTheDocument()
    expect(screen.getByText('已选择套餐：标准版')).toBeInTheDocument()
  })

  it('turns the claim entry into an explicit post-registration instruction', async () => {
    const user = userEvent.setup()
    render(
      <MemoryRouter initialEntries={['/login?tab=register']}>
        <LoginPage />
      </MemoryRouter>,
    )

    await user.click(screen.getByRole('button', { name: '主动领取体验积分' }))
    expect(
      screen.getByText(/完成注册后，请在控制台主动领取体验积分/),
    ).toBeInTheDocument()
  })
})
