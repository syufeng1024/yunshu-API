import { cleanup, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { Announcement } from '@/api/admin'
import { renderWithProviders } from '@/test/renderConsole'
import AdminAnnouncementsPage from './AnnouncementsPage'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  }
})

const mockedGet = vi.mocked(api.get)

function announcement(overrides: Partial<Announcement>): Announcement {
  return {
    id: 31,
    title: '草稿标题',
    content: '草稿内容',
    ann_type: 'NOTICE',
    position_mask: 2,
    pinned: false,
    effective_from: '2026-10-20T02:00:00Z',
    effective_to: null,
    state: 'DRAFT',
    created_by: 9001,
    created_at: '2026-10-20T02:00:00Z',
    updated_at: '2026-10-20T02:00:00Z',
    ...overrides,
  }
}

const draft = announcement({})
const published = announcement({
  id: 32,
  title: '已发布标题',
  ann_type: 'PRICE_CHANGE',
  effective_from: '2026-11-01T00:00:00Z',
  state: 'PUBLISHED',
})
const scheduled = announcement({
  id: 33,
  title: '已定时标题',
  effective_from: '2026-11-03T00:00:00Z',
  state: 'SCHEDULED',
})

const emptyAudit = {
  items: [],
  page: 1,
  page_size: 50,
  total: 0,
  from: '2026-10-21T00:00:00Z',
  to: '2026-10-28T00:00:00Z',
}

beforeEach(() => {
  mockedGet.mockReset()
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/admin/announcements?')) {
      if (path.includes('state=DRAFT')) {
        return Promise.resolve({ items: [draft], page: 1, page_size: 20, total: 1 })
      }
      return Promise.resolve({
        items: [draft, published, scheduled],
        page: 1,
        page_size: 20,
        total: 3,
      })
    }
    if (path.startsWith('/api/admin/audit-logs?')) return Promise.resolve(emptyAudit)
    return Promise.reject(new Error(`unexpected ${path}`))
  })
})

afterEach(cleanup)

describe('AnnouncementsPage', () => {
  it('AnnouncementsPage：草稿区展示 DRAFT，已发布不可编辑', async () => {
    const user = userEvent.setup()
    renderWithProviders(<AdminAnnouncementsPage />)

    expect(await screen.findByText('草稿标题')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '全部公告' }))

    const row = (await screen.findByText('已发布标题')).closest('tr')
    expect(row).not.toBeNull()
    const publishedRow = within(row as HTMLElement)
    expect(publishedRow.getByRole('button', { name: '编辑' })).toBeDisabled()
    expect(publishedRow.getByRole('button', { name: '发布' })).toBeDisabled()
    expect(publishedRow.getByText('已发布不可编辑、无需重复发布')).toBeInTheDocument()
  })

  it('AnnouncementsPage：定时公告只展示说明，不在前端改状态', async () => {
    const user = userEvent.setup()
    renderWithProviders(<AdminAnnouncementsPage />)

    await screen.findByText('草稿标题')
    await user.click(screen.getByRole('tab', { name: '全部公告' }))

    const row = (await screen.findByText('已定时标题')).closest('tr')
    expect(row).not.toBeNull()
    expect(within(row as HTMLElement).getByText(/已定时，将于/)).toBeInTheDocument()
    expect(within(row as HTMLElement).getByRole('button', { name: '发布' })).toBeEnabled()
  })
})
