import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { PublicModel } from '@/api/public'
import { usePublicModels } from '@/api/public'
import { fixtureResponses } from '@/fixtures/public'
import { TOOL_INTEGRATIONS } from '@/content/toolIntegrations'
import DocsPage from './DocsPage'

vi.mock('@/api/public', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/public')>()
  return { ...actual, usePublicModels: vi.fn() }
})

const models = fixtureResponses['/api/public/models'].data

const imageModel: PublicModel = {
  ...models[0],
  id: 'mdl_image_demo',
  model_name: 'image-demo',
  display_name: 'Image Demo',
  model_type: 'IMAGE',
}

const mockedUseModels = vi.mocked(usePublicModels)

function modelsResult(data: readonly PublicModel[]) {
  return {
    data: { data },
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  } as unknown as ReturnType<typeof usePublicModels>
}

beforeEach(() => {
  mockedUseModels.mockReset()
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('DocsPage', () => {
  it('F3-3: shows the virtual-value notice', () => {
    mockedUseModels.mockReturnValue(modelsResult(models))
    render(
      <MemoryRouter initialEntries={['/docs']}>
        <DocsPage />
      </MemoryRouter>,
    )
    expect(screen.getByText('示例为虚拟值')).toBeInTheDocument()
    expect(screen.getAllByText(/均为虚拟值/).length).toBeGreaterThan(0)
  })

  it('hides chapter 5 when no LISTED image model exists', () => {
    mockedUseModels.mockReturnValue(modelsResult(models))
    render(
      <MemoryRouter initialEntries={['/docs']}>
        <DocsPage />
      </MemoryRouter>,
    )
    expect(screen.queryByRole('button', { name: /图像生成/ })).not.toBeInTheDocument()
    expect(screen.getByText(/8 个章节/)).toBeInTheDocument()
  })

  it('F1-6: never puts a CATALOG_ONLY model into the documentation examples', () => {
    const catalogOnly: PublicModel = {
      ...models[0],
      id: 'mdl_catalog_docs',
      model_name: 'catalog-only-docs',
      display_name: 'Catalog Only Docs',
      listing_state: 'CATALOG_ONLY',
    }
    mockedUseModels.mockReturnValue(modelsResult([catalogOnly]))
    render(
      <MemoryRouter initialEntries={['/docs']}>
        <DocsPage />
      </MemoryRouter>,
    )

    expect(screen.queryByText(/catalog-only-docs/)).not.toBeInTheDocument()
    expect(screen.getAllByText(/<模型名>/).length).toBeGreaterThan(0)
  })

  it('shows chapter 5 when a LISTED image model exists', async () => {
    const user = userEvent.setup()
    mockedUseModels.mockReturnValue(modelsResult([...models, imageModel]))
    render(
      <MemoryRouter initialEntries={['/docs']}>
        <DocsPage />
      </MemoryRouter>,
    )

    expect(screen.getByText(/9 个章节/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /图像生成/ }))

    expect(screen.getByRole('heading', { level: 2, name: '图像生成' })).toBeInTheDocument()
    expect(screen.getAllByText(/image-demo/).length).toBeGreaterThan(0)
  })

  it('F3-2: switches chapters on the client without a reload', async () => {
    const user = userEvent.setup()
    mockedUseModels.mockReturnValue(modelsResult(models))
    render(
      <MemoryRouter initialEntries={['/docs']}>
        <DocsPage />
      </MemoryRouter>,
    )

    await user.click(screen.getByRole('button', { name: /鉴权与请求头/ }))
    expect(screen.getByRole('heading', { level: 2, name: '鉴权与请求头' })).toBeInTheDocument()
    expect(screen.getByText('Authorization')).toBeInTheDocument()
  })

  it('F4-5: chapter 9 renders the shared five-tool content source', async () => {
    const user = userEvent.setup()
    mockedUseModels.mockReturnValue(modelsResult(models))
    render(
      <MemoryRouter initialEntries={['/docs']}>
        <DocsPage />
      </MemoryRouter>,
    )

    await user.click(screen.getByRole('button', { name: /工具集成/ }))
    for (const tool of TOOL_INTEGRATIONS) {
      expect(screen.getByRole('heading', { level: 3, name: tool.name })).toBeInTheDocument()
    }
  })

  it('renders ErrorState when the model list fails', () => {
    mockedUseModels.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error('network'),
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof usePublicModels>)
    render(
      <MemoryRouter initialEntries={['/docs']}>
        <DocsPage />
      </MemoryRouter>,
    )
    expect(screen.getByRole('alert')).toHaveTextContent('请求失败')
  })
})
