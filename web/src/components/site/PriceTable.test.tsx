import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { PublicModel } from '@/api/public'
import { fixtureResponses } from '@/fixtures/public'
import { PriceTable } from './PriceTable'

const models = fixtureResponses['/api/public/models'].data

const imageModel: PublicModel = {
  ...models[0],
  id: 'mdl_image_demo',
  model_name: 'image-demo',
  display_name: 'Image Demo',
  model_type: 'IMAGE',
}

const catalogOnlyText: PublicModel = {
  ...models[0],
  id: 'mdl_catalog_text',
  model_name: 'catalog-text-demo',
  display_name: 'Catalog Text Demo',
  listing_state: 'CATALOG_ONLY',
}

const catalogOnlyImage: PublicModel = {
  ...imageModel,
  id: 'mdl_catalog_image',
  model_name: 'catalog-image-demo',
  display_name: 'Catalog Image Demo',
  listing_state: 'CATALOG_ONLY',
}

describe('PriceTable', () => {
  it('F1-1 / F1-6: the image tab shows only LISTED image models', async () => {
    const user = userEvent.setup()
    render(
      <PriceTable
        models={[...models, imageModel, catalogOnlyText, catalogOnlyImage]}
      />,
    )

    expect(screen.getByText('Qwen Max')).toBeInTheDocument()
    expect(screen.queryByText('Image Demo')).not.toBeInTheDocument()
    expect(screen.queryByText('Catalog Text Demo')).not.toBeInTheDocument()

    await user.click(screen.getByRole('tab', { name: '图像' }))

    expect(screen.getByText('Image Demo')).toBeInTheDocument()
    expect(screen.queryByText('Qwen Max')).not.toBeInTheDocument()
    expect(screen.queryByText('Catalog Image Demo')).not.toBeInTheDocument()
    expect(screen.queryByText('Catalog Text Demo')).not.toBeInTheDocument()
  })

  it('renders an empty state for a successful empty image group', async () => {
    const user = userEvent.setup()
    render(<PriceTable models={models} />)
    await user.click(screen.getByRole('tab', { name: '图像' }))
    expect(screen.getByText('暂无已上架图像模型')).toBeInTheDocument()
  })

  it('renders ErrorState instead of a zero-value table when the request fails', () => {
    render(<PriceTable models={[]} error="网络连接失败，请检查网络后重试。" />)
    expect(screen.getByRole('alert')).toHaveTextContent('网络连接失败')
    expect(screen.queryByText('暂无已上架文本模型')).not.toBeInTheDocument()
  })

  it('F1-2: renders price values from props, not from a page literal', () => {
    render(
      <PriceTable
        models={[
          {
            ...models[0],
            id: 'mdl_dynamic_price',
            input_price_micro_per_k: 1234,
          },
        ]}
      />,
    )
    expect(screen.getByText('0.1234 积分/千 token')).toBeInTheDocument()
  })
})
