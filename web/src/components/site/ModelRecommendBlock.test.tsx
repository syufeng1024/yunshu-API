import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { PublicModel } from '@/api/public'
import { fixtureResponses } from '@/fixtures/public'
import { ModelRecommendBlock } from './ModelRecommendBlock'

const models = fixtureResponses['/api/public/models'].data

function weighted(
  id: string,
  weight: number,
  publishedAt = '2026-01-01T00:00:00Z',
): PublicModel {
  return {
    ...models[0],
    id,
    display_name: id,
    model_name: id,
    recommend_weight: weight,
    published_at: publishedAt,
  }
}

describe('ModelRecommendBlock', () => {
  it('F1-7: does not render at all when every weight is zero', () => {
    const zero = models.map((model) => ({ ...model, recommend_weight: 0 }))
    const { container } = render(<ModelRecommendBlock models={zero} />)
    expect(container.firstChild).toBeNull()
  })

  it('F1-7: orders cards by weight and then by publish time', () => {
    render(
      <ModelRecommendBlock
        models={[
          weighted('weight-10', 10),
          weighted('weight-30-old', 30, '2026-01-01T00:00:00Z'),
          weighted('weight-30-new', 30, '2026-02-01T00:00:00Z'),
        ]}
      />,
    )
    const headings = screen.getAllByRole('heading', { level: 3 }).map((node) => node.textContent)
    expect(headings).toEqual(['weight-30-new', 'weight-30-old', 'weight-10'])
  })

  it('caps the block at eight cards', () => {
    const many = Array.from({ length: 12 }, (_, index) =>
      weighted(`model-${index}`, index + 1),
    )
    render(<ModelRecommendBlock models={many} />)
    expect(screen.getAllByRole('article')).toHaveLength(8)
  })

  it('renders ErrorState when recommendations fail to load', () => {
    render(<ModelRecommendBlock models={[]} error="推荐位加载失败" />)
    expect(screen.getByRole('alert')).toHaveTextContent('推荐位加载失败')
  })
})
