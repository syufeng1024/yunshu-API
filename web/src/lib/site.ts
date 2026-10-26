import type {
  AnnouncementCategory,
  ApiProtocol,
  ModelType,
  PlanCode,
  PublicModel,
  PublicPlan,
} from '@/api/public'
import { ROUTES } from './constants'

export const RECOMMEND_LIMIT = 8

/** The four purchasable tiers. FREE_DEFAULT is a rate policy, not a card. */
export const PLAN_CODES: readonly PlanCode[] = ['TRIAL', 'STANDARD', 'PRO', 'ENTERPRISE']

export const MODEL_TYPE_LABELS: Record<ModelType, string> = {
  TEXT: '文本',
  IMAGE: '图像',
}

export const PROTOCOL_LABELS: Record<ApiProtocol, string> = {
  OPENAI: 'OpenAI',
  ANTHROPIC: 'Anthropic',
}

export const ANNOUNCEMENT_CATEGORY_LABELS: Record<AnnouncementCategory, string> = {
  PRICE: '价格调整',
  RATE_LIMIT: '限流调整',
  GENERAL: '公告',
}

/** Public pages only render LISTED models (F1-6). */
export function selectListedModels(models: readonly PublicModel[]): PublicModel[] {
  return models.filter((model) => model.listing_state === 'LISTED')
}

export function selectModelsByType(
  models: readonly PublicModel[],
  type: ModelType,
): PublicModel[] {
  return selectListedModels(models).filter((model) => model.model_type === type)
}

function publishedTime(model: PublicModel): number {
  const value = Date.parse(model.published_at)
  return Number.isNaN(value) ? 0 : value
}

/** F1-7: (recommend_weight DESC, published_at DESC). */
export function sortModelsForDisplay(models: readonly PublicModel[]): PublicModel[] {
  return [...selectListedModels(models)].sort(
    (a, b) => b.recommend_weight - a.recommend_weight || publishedTime(b) - publishedTime(a),
  )
}

/**
 * Top-weighted models for the home page. All-zero weights means the section is
 * not rendered at all, rather than reserving an empty slot (F1-7).
 */
export function selectRecommendedModels(
  models: readonly PublicModel[],
  limit = RECOMMEND_LIMIT,
): PublicModel[] {
  return sortModelsForDisplay(models)
    .filter((model) => model.recommend_weight > 0)
    .slice(0, limit)
}

/** Chapter 5 of /docs only exists while a LISTED image model exists. */
export function hasListedImageModel(models: readonly PublicModel[]): boolean {
  return selectModelsByType(models, 'IMAGE').length > 0
}

export function uniqueProtocols(models: readonly PublicModel[]): ApiProtocol[] {
  const protocols = new Set<ApiProtocol>()
  for (const model of selectListedModels(models)) {
    for (const protocol of model.protocols) protocols.add(protocol)
  }
  return [...protocols]
}

export function selectPurchasablePlans(plans: readonly PublicPlan[]): PublicPlan[] {
  const byCode = new Map(plans.map((plan) => [plan.code, plan]))
  return PLAN_CODES.flatMap((code) => {
    const plan = byCode.get(code)
    return plan ? [plan] : []
  })
}

export function planScopeLabel(mode: string): string {
  return mode === 'ALL' ? '全部已上架模型' : '按运营配置的模型范围'
}

export function invoiceLabel(plan: PublicPlan): string {
  return plan.invoice_supported === 1 ? '支持' : '不支持'
}

export function slaLabel(plan: PublicPlan): string {
  return plan.sla_text || '不含 SLA'
}

/**
 * F2-1: anonymous visitors register with the plan marker; authenticated users
 * go straight to the recharge page with the same marker preselected.
 */
export function planCtaPath(planCode: PlanCode, authenticated: boolean): string {
  const query = new URLSearchParams({ plan: planCode })
  if (authenticated) return `${ROUTES.consoleRecharge}?${query.toString()}`
  query.set('tab', 'register')
  return `${ROUTES.login}?${query.toString()}`
}
