import { useQuery } from '@tanstack/react-query'
import { api } from './client'

/**
 * Public site contract. The control plane does not expose these endpoints yet
 * (T12/T15); the dev-only fixture branch in client.ts fills the same shapes so
 * the site can be reviewed without a backend.
 */

export type ModelType = 'TEXT' | 'IMAGE'
export type ListingState = 'LISTED' | 'CATALOG_ONLY' | 'DELISTED'
export type ApiProtocol = 'OPENAI' | 'ANTHROPIC'
export type PlanCode = 'TRIAL' | 'STANDARD' | 'PRO' | 'ENTERPRISE' | 'FREE_DEFAULT'
export type AnnouncementCategory = 'PRICE' | 'RATE_LIMIT' | 'GENERAL'
export type EndpointRole = 'PRIMARY' | 'BACKUP'
export type HealthStatus = 'PASS' | 'FAIL' | null

export interface PublicModel {
  id: string
  model_name: string
  display_name: string
  model_type: ModelType
  listing_state: ListingState
  context_window: number
  input_price_micro_per_k: number
  output_price_micro_per_k: number
  protocols: ApiProtocol[]
  recommend_weight: number
  published_at: string
}

export interface PublicModelsResponse {
  data: PublicModel[]
}

export interface PublicPlan {
  code: PlanCode
  name: string
  price_fen: number
  grant_recharge_micro: number
  grant_bonus_micro: number
  bonus_valid_days: number
  rpm: number
  tpm: number
  concurrency: number
  daily_quota_micro: number | null
  model_scope_mode: string
  invoice_supported: 0 | 1
  sla_text: string
  permanent: 0 | 1
  sort_order: number
}

export interface PublicPlansResponse {
  data: PublicPlan[]
}

export interface PublicAnnouncement {
  id: string
  title: string
  body: string
  category: AnnouncementCategory
  effective_at: string
  published_at: string
}

export interface PublicAnnouncementsResponse {
  data: PublicAnnouncement[]
}

export interface PublicEndpoint {
  id: string
  name: string
  base_url: string
  line: string
  description: string
  role: EndpointRole
  status_page_url: string
  health_status: HealthStatus
}

export interface PublicEndpointsResponse {
  data: PublicEndpoint[]
}

export interface PublicIntegrationLink {
  tool_key: string
  /** Backend-owned template. The browser never knows the parameter names. */
  deep_link_template: string | null
  enabled: boolean
}

export interface PublicIntegrationLinksResponse {
  data: PublicIntegrationLink[]
}

export interface ImportCredentials {
  api_key: string
  base_url: string
  model: string
}

export function fetchPublicModels(): Promise<PublicModelsResponse> {
  return api.get<PublicModelsResponse>('/api/public/models')
}

export function fetchPublicPlans(): Promise<PublicPlansResponse> {
  return api.get<PublicPlansResponse>('/api/public/plans')
}

export function fetchPublicAnnouncements(): Promise<PublicAnnouncementsResponse> {
  return api.get<PublicAnnouncementsResponse>('/api/public/announcements')
}

export function fetchPublicEndpoints(): Promise<PublicEndpointsResponse> {
  return api.get<PublicEndpointsResponse>('/api/public/endpoints')
}

export function fetchPublicIntegrationLinks(): Promise<PublicIntegrationLinksResponse> {
  return api.get<PublicIntegrationLinksResponse>('/api/public/integration-links')
}

/**
 * Fetches import credentials at click time. The response is used once to build
 * the deep link and is never stored, rendered or tracked (F4-2).
 */
export function requestImportCredentials(toolKey: string): Promise<ImportCredentials> {
  return api.post<ImportCredentials>('/api/v1/integrations/import-credentials', {
    tool_key: toolKey,
  })
}

export function usePublicModels() {
  return useQuery({
    queryKey: ['public', 'models'],
    queryFn: fetchPublicModels,
  })
}

export function usePublicPlans() {
  return useQuery({
    queryKey: ['public', 'plans'],
    queryFn: fetchPublicPlans,
  })
}

export function usePublicAnnouncements() {
  return useQuery({
    queryKey: ['public', 'announcements'],
    queryFn: fetchPublicAnnouncements,
  })
}

export function usePublicEndpoints() {
  return useQuery({
    queryKey: ['public', 'endpoints'],
    queryFn: fetchPublicEndpoints,
  })
}
