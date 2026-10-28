import { cleanup, fireEvent, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '@/api/client'
import type { Announcement } from '@/api/admin'
import { ToastViewport } from '@/components'
import { useToastStore } from '@/stores/toast'
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
const mockedPost = vi.mocked(api.post)

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
    model_ids: [],
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
const offline = announcement({
  id: 34,
  title: '已下线标题',
  effective_from: '2026-10-15T00:00:00Z',
  state: 'OFFLINE',
})

function model(id: number, name: string) {
  return {
    id,
    model_name: name,
    display_name: name,
    region: 'DOMESTIC',
    model_type: 'TEXT',
    protocol_id: 1,
    protocol: 'OPENAI_COMPAT',
    context_length: 128000,
    capability_mask: 0,
    listing_state: 'LISTED',
    recommend_weight: 0,
    aliases: [],
    pricing: null,
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
  }
}

const modelFixtures = [model(1, 'deepseek-chat'), model(2, 'qwen-plus')]

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
  mockedPost.mockReset()
  useToastStore.setState({ toasts: [] })
  mockedGet.mockImplementation((path: string) => {
    if (path.startsWith('/api/admin/announcements?')) {
      if (path.includes('state=DRAFT')) {
        return Promise.resolve({ items: [draft], page: 1, page_size: 20, total: 1 })
      }
      return Promise.resolve({
        items: [draft, published, scheduled, offline],
        page: 1,
        page_size: 20,
        total: 4,
      })
    }
    if (path.startsWith('/api/admin/models?')) {
      return Promise.resolve({
        items: modelFixtures,
        page: 1,
        page_size: 100,
        total: modelFixtures.length,
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

  it('AnnouncementsPage：下线需要二次确认，已下线置灰', async () => {
    mockedPost.mockResolvedValue({
      announcement: { ...scheduled, state: 'OFFLINE' },
    })
    const user = userEvent.setup()
    renderWithProviders(
      <>
        <AdminAnnouncementsPage />
        <ToastViewport />
      </>,
    )
    await screen.findByText('草稿标题')
    await user.click(screen.getByRole('tab', { name: '全部公告' }))

    const offlineRow = (await screen.findByText('已下线标题')).closest('tr')
    expect(offlineRow).not.toBeNull()
    expect(
      within(offlineRow as HTMLElement).getByRole('button', { name: '下线' }),
    ).toBeDisabled()

    const scheduledRow = (await screen.findByText('已定时标题')).closest('tr')
    expect(scheduledRow).not.toBeNull()
    await user.click(
      within(scheduledRow as HTMLElement).getByRole('button', { name: '下线' }),
    )
    expect(await screen.findByText('下线公告')).toBeInTheDocument()
    expect(mockedPost).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: '确认下线' }))
    expect(await screen.findByText('公告已下线')).toBeInTheDocument()
    expect(mockedPost.mock.calls.at(-1)?.[0]).toBe(
      '/api/admin/announcements/33/offline',
    )
  })

  it('AnnouncementsPage：effective_from 可编辑且不能早于 publish_at', async () => {
    const user = userEvent.setup()
    renderWithProviders(<AdminAnnouncementsPage />)
    await screen.findByText('草稿标题')
    await user.click(screen.getByRole('tab', { name: '全部公告' }))

    const row = (await screen.findByText('已定时标题')).closest('tr')
    await user.click(within(row as HTMLElement).getByRole('button', { name: '编辑' }))

    const effectiveFrom = await screen.findByLabelText('生效时间（可选）')
    expect(effectiveFrom).toBeEnabled()
    const publishAt = screen.getByLabelText('定时发布时间（可选）')
    fireEvent.change(publishAt, { target: { value: '2026-11-05T10:00' } })
    fireEvent.change(effectiveFrom, { target: { value: '2026-11-01T10:00' } })
    await user.click(screen.getByRole('button', { name: '保存' }))

    expect(
      await screen.findByText('生效时间不能早于定时发布时间。'),
    ).toBeInTheDocument()
  })

  it('AnnouncementsPage：活动按已关联模型筛出同模型调价公告', async () => {
    const from = new Date(Date.now() + 24 * 60 * 60 * 1_000).toISOString()
    const to = new Date(Date.now() + 3 * 24 * 60 * 60 * 1_000).toISOString()
    const promotion = announcement({
      id: 41,
      title: '活动草稿',
      ann_type: 'PROMOTION',
      model_ids: [1],
    })
    const sameModel = announcement({
      id: 51,
      title: '调价公告A',
      ann_type: 'PRICE_CHANGE',
      state: 'PUBLISHED',
      effective_from: from,
      effective_to: to,
      model_ids: [1],
    })
    const otherModel = announcement({
      id: 52,
      title: '调价公告B',
      ann_type: 'PRICE_CHANGE',
      state: 'PUBLISHED',
      effective_from: from,
      effective_to: to,
      model_ids: [2],
    })

    mockedGet.mockImplementation((path: string) => {
      if (path.startsWith('/api/admin/announcements?')) {
        if (path.includes('page_size=100')) {
          return Promise.resolve({
            items: [sameModel, otherModel],
            page: 1,
            page_size: 100,
            total: 2,
          })
        }
        return Promise.resolve({ items: [promotion], page: 1, page_size: 20, total: 1 })
      }
      if (path.startsWith('/api/admin/models?')) {
        return Promise.resolve({
          items: modelFixtures,
          page: 1,
          page_size: 100,
          total: modelFixtures.length,
        })
      }
      if (path.startsWith('/api/admin/audit-logs?')) return Promise.resolve(emptyAudit)
      return Promise.reject(new Error(`unexpected ${path}`))
    })

    const user = userEvent.setup()
    renderWithProviders(<AdminAnnouncementsPage />)
    const row = (await screen.findByText('活动草稿')).closest('tr')
    expect(row).not.toBeNull()
    await user.click(within(row as HTMLElement).getByRole('button', { name: '编辑' }))

    expect(await screen.findByText('活动与调价冲突')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'deepseek-chat' })).toBeChecked()
    expect(await screen.findByText(/调价公告A/)).toBeInTheDocument()
    expect(screen.queryByText(/调价公告B/)).not.toBeInTheDocument()
  })
})
