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

  // D-24: /api/public/endpoints is a deliberate empty stub in V1.0 (there is no
  // t_endpoint table). The hero must not advertise a literal 0, and the same
  // applies to the model and protocol counts when their data is empty.
  it('D-24: empty endpoints drop the line cell, empty counts render --, never a literal 0', () => {
    mockedEndpoints.mockReturnValue(result([]) as ReturnType<typeof usePublicEndpoints>)
    mockedModels.mockReturnValue(result([]) as ReturnType<typeof usePublicModels>)

    render(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    )

    const stats = screen.getByTestId('hero-stats')
    // 没有接入点时整格不渲染：「筹备中」夹在两个数字中间读起来像少填了一格，
    // 也等于主动告诉访客「还没上线」。数据条只放真实数字。
    expect(stats.textContent).not.toContain('接入线路')
    expect(stats.textContent).not.toContain('筹备中')
    expect(stats.children).toHaveLength(2)
    // 两格时必须是两列；漏掉这层断言，一旦有人把类名固定回 grid-cols-3，
    // 空格子会被拉成等宽的三列，首屏右侧又多出一块空白。
    expect(stats.className).toContain('grid-cols-2')
    expect(stats.className).not.toContain('grid-cols-3')
    // 模型与协议都为空，两格各自回落成「--」。
    expect(stats.textContent).toContain('--')
    expect(stats.textContent ?? '').not.toMatch(/(^|\D)0(\D|$)/)
  })

  // 加载态的骨架屏数量也必须跟着格数走：写死 3 格会在数据到位后收回 2 格，
  // 首屏闪一下「多一格又少一格」，正好落在用户反馈的「首页上方空白」位置。
  it('uses a two-cell skeleton while the hero stats are loading', () => {
    mockedModels.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
      error: null,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof usePublicModels>)
    mockedEndpoints.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
      error: null,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof usePublicEndpoints>)

    render(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    )

    const stats = screen.getByTestId('hero-stats')
    // 两个骨架单元（与没有接入点时的真实格数一致）……
    expect(stats.children).toHaveLength(2)
    // ……每个单元两条脉冲条（标签条 + 数值条），共 4 条。
    expect(stats.querySelectorAll('.animate-pulse')).toHaveLength(4)
    expect(stats.className).toContain('grid-cols-2')
  })

  it('D-24: keeps the live label and counts when endpoints exist', () => {
    render(
      <MemoryRouter>
        <LandingPage />
      </MemoryRouter>,
    )

    const stats = screen.getByTestId('hero-stats')
    expect(stats.textContent).toContain('接入线路')
    expect(stats.textContent).not.toContain('筹备中')
    expect(stats.textContent).toContain(String(endpoints.length))
    // 有接入点时三项都在，仍是三列。
    expect(stats.children).toHaveLength(3)
    expect(stats.className).toContain('grid-cols-3')
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
