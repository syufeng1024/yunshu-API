import { describe, expect, it } from 'vitest'
import type { PublicModel } from '@/api/public'
import { fixtureResponses } from '@/fixtures/public'
import {
  hasListedImageModel,
  planCtaPath,
  selectListedModels,
  selectModelsByType,
  selectPurchasablePlans,
  selectRecommendedModels,
  sortModelsForDisplay,
} from './site'

const models = fixtureResponses['/api/public/models'].data
const plans = fixtureResponses['/api/public/plans'].data

const imageModel: PublicModel = {
  ...models[0],
  id: 'mdl_image_demo',
  model_name: 'image-demo',
  display_name: 'Image Demo',
  model_type: 'IMAGE',
  listing_state: 'LISTED',
  recommend_weight: 0,
}

const catalogOnlyText: PublicModel = {
  ...models[0],
  id: 'mdl_catalog_only',
  model_name: 'catalog-only-demo',
  listing_state: 'CATALOG_ONLY',
  recommend_weight: 999,
}

const catalogOnlyImage: PublicModel = {
  ...imageModel,
  id: 'mdl_catalog_image',
  model_name: 'catalog-image-demo',
  listing_state: 'CATALOG_ONLY',
}

describe('public model selectors', () => {
  it('F1-1: an image group contains only LISTED image models', () => {
    const rows = selectModelsByType([...models, imageModel, catalogOnlyText, catalogOnlyImage], 'IMAGE')
    expect(rows.map((row) => row.model_name)).toEqual(['image-demo'])
    expect(rows.some((row) => row.model_type === 'TEXT')).toBe(false)
  })

  it('F1-6: CATALOG_ONLY is removed from visible lists and recommendations', () => {
    const all = [...models, catalogOnlyText, catalogOnlyImage]
    expect(selectListedModels(all).map((row) => row.id)).not.toContain('mdl_catalog_only')
    expect(sortModelsForDisplay(all).map((row) => row.id)).not.toContain('mdl_catalog_image')
    expect(selectRecommendedModels(all).map((row) => row.id)).not.toContain('mdl_catalog_only')
    expect(hasListedImageModel([...models, catalogOnlyImage])).toBe(false)
    expect(hasListedImageModel([...models, imageModel])).toBe(true)
  })

  it('F1-7: all-zero weights hide the block; otherwise sort by weight then publish time', () => {
    const zero = models.map((model) => ({ ...model, recommend_weight: 0 }))
    expect(selectRecommendedModels(zero)).toEqual([])

    const sorted = selectRecommendedModels([
      { ...models[0], id: 'a', recommend_weight: 10, published_at: '2026-01-01T00:00:00Z' },
      { ...models[0], id: 'b', recommend_weight: 30, published_at: '2026-01-01T00:00:00Z' },
      { ...models[0], id: 'c', recommend_weight: 30, published_at: '2026-02-01T00:00:00Z' },
    ])
    expect(sorted.map((model) => model.id)).toEqual(['c', 'b', 'a'])
  })

  it('caps the recommendation block at eight models', () => {
    const many = Array.from({ length: 12 }, (_, index) => ({
      ...models[0],
      id: `model-${index}`,
      recommend_weight: index + 1,
      published_at: '2026-01-01T00:00:00Z',
    }))
    expect(selectRecommendedModels(many)).toHaveLength(8)
  })
})

describe('plan selectors', () => {
  it('keeps the four purchasable tiers in PRD order and drops FREE_DEFAULT', () => {
    const rows = selectPurchasablePlans(plans)
    expect(rows.map((plan) => plan.code)).toEqual(['TRIAL', 'STANDARD', 'PRO', 'ENTERPRISE'])
    expect(rows.some((plan) => plan.code === 'FREE_DEFAULT')).toBe(false)
  })

  it('F2-1: plan CTA carries the plan marker for both auth states', () => {
    const anonymous = planCtaPath('STANDARD', false)
    expect(anonymous.startsWith('/login?')).toBe(true)
    expect(anonymous).toContain('plan=STANDARD')
    expect(anonymous).toContain('tab=register')

    const authenticated = planCtaPath('PRO', true)
    expect(authenticated).toBe('/console/recharge?plan=PRO')
  })
})
