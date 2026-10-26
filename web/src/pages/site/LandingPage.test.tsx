import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  usePublicAnnouncements,
  usePublicEndpoints,
  usePublicModels,
  usePublicPlans,
} from '@/api/public'
import { fixtureResponses } from '@/fixtures/public'
import LandingPage from './LandingPage'

vi.mock('@/api/public', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/public')>()
  return {
    ...actual,
    usePublicModels: vi.fn(),
    usePublicPlans: vi.fn(),
    usePublicAnnouncements: vi.fn(),
    usePublicEndpoints: vi.fn(),
  }
})

const models = fixtureResponses['/api/public/models'].data
const plans = fixtureResponses['/api/public/plans'].data
const announcements = fixtureResponses['/api/public/announcements'].data
const endpoints = fixtureResponses['/api/public/endpoints'].data

const mockedModels = vi.mocked(usePublicModels)
const mockedPlans = vi.mocked(usePublicPlans)
const mockedAnnouncements = vi.mocked(usePublicAnnouncements)
const mockedEndpoints = vi.mocked(usePublicEndpoints)

function result<T>(data: T) {
  return {
    data: { data },
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  } as unknown
}

beforeEach(() => {
  mockedModels.mockReturnValue(result(models) as ReturnType<typeof usePublicModels>)
  mockedPlans.mockReturnValue(result(plans) as ReturnType<typeof usePublicPlans>)
  mockedAnnouncements.mockReturnValue(
    result(announcements) as ReturnType<typeof usePublicAnnouncements>,
  )
  mockedEndpoints.mockReturnValue(result(endpoints) as ReturnType<typeof usePublicEndpoints>)
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('LandingPage', () => {
  it('A20: renders every required section plus the recommendation block', () => {
    render(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    )

    const ids = Array.from(document.querySelectorAll('[data-section]')).map((node) =>
      node.getAttribute('data-section'),
    )
    expect(ids).toEqual(
      expect.arrayContaining([
        'hero',
        'capabilities',
        'recommendations',
        'models',
        'plans',
        'announcements',
        'endpoints',
        'tools',
        'quickstart',
        'cta',
      ]),
    )
  })

  it('A4/A5: does not mention availability in endpoints or contract/internal prices', () => {
    const { container } = render(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    )
    const text = container.textContent ?? ''
    expect(text).not.toMatch(/合约价|内部价/)
    expect(text).not.toMatch(/更多工具|即将支持|注册即送/)
    const endpointSection = document.querySelector('[data-section="endpoints"]')
    expect(endpointSection?.textContent).not.toMatch(/可用|正常|在线/)
  })

  it('renders announcements with an effective time and four plan rows', () => {
    render(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    )
    expect(screen.getByText(/^生效时间：/)).toBeInTheDocument()
    expect(screen.queryByText('默认档')).not.toBeInTheDocument()
  })
})
