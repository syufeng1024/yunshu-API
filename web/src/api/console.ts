import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { api } from './client'

/**
 * Console contracts, transcribed from internal/control/{user,key,model}.
 * Field names are the JSON tags, not the Go names: they are what the API
 * actually returns and what docs/control-api.md documents.
 */

export interface Page<T> {
  items: T[]
  page: number
  page_size: number
  total: number
}

// ---------------------------------------------------------------------------
// Overview (GET /api/console/overview)
// ---------------------------------------------------------------------------

export interface OverviewBalance {
  micro: number
  credits: string
  cny: string
  recharge_micro: number
  bonus_micro: number
}

export interface OverviewToday {
  requests: number
  consumed_micro: number
  consumed_credits: string
  /** null when today has no billable attempts; render "—", never 0%. */
  success_rate_bp: number | null
}

export interface OverviewHour {
  hour: string
  requests: number
  consumed_micro: number
}

export interface OverviewPlan {
  code: string
  name: string
  policy_source: string
  trial_expire_at: string | null
}

export interface OverviewQuota {
  rpm: number
  tpm: number
  concurrency: number
  /** null = the plan has no daily ceiling. */
  daily_quota_micro: number | null
  used_today_micro: number
  remaining_micro: number | null
}

export type EtaReason = 'OK' | 'QUOTA_REACHED' | 'NO_DAILY_QUOTA' | 'NO_RECENT_USAGE' | string

export interface OverviewEta {
  reachable: boolean
  reason: EtaReason
  eta_at: string | null
  seconds_left: number | null
  burn_micro_per_hour: number
}

export interface ConsoleOverview {
  balance: OverviewBalance
  today: OverviewToday
  hourly: OverviewHour[]
  plan: OverviewPlan
  quota: OverviewQuota
  eta: OverviewEta
}

// ---------------------------------------------------------------------------
// Keys (GET/POST/PATCH/DELETE /api/console/keys)
// ---------------------------------------------------------------------------

export type KeyStatus = 'ENABLED' | 'DISABLED'
export type ModelScopeMode = 'ALL' | 'SELECTED'

export interface KeyListItem {
  id: number
  key_name: string
  /** prefix + "****" + last4, assembled server-side. */
  key_masked: string
  status: KeyStatus
  expired: boolean
  group_code?: string
  group_ratio_bp: number | null
  model_scope_mode: ModelScopeMode
  model_count: number
  ip_whitelist_mode: string
  ip_rule_count: number
  expire_at: string | null
  today_consumed_micro: number
  created_at: string
  last_used_at: string | null
}

export type KeyListResponse = Page<KeyListItem>

export interface CreatedKey {
  id: number
  key_name: string
  /** Plaintext. Present only in the create/rotate response. */
  key: string
  key_prefix: string
  key_last4: string
  key_masked: string
  model_scope_mode: ModelScopeMode
  model_ids: number[]
  group_code: string
  expire_at: string | null
  ip_allowlist: string[]
}

export interface CreateKeysResponse {
  items: CreatedKey[]
  count: number
  warning: string
}

export interface CreateKeysInput {
  name: string
  count: number
  model_scope_mode: ModelScopeMode
  /** Omitted entirely for ALL: the backend rejects ALL + model_ids. */
  model_ids?: number[]
  expire_at: string
  ip_allowlist: string[]
}

/** PRD F6-5: one batch is 1–10 keys. The backend rejects anything outside. */
export const KEY_BATCH_MIN = 1
export const KEY_BATCH_MAX = 10

/**
 * Free-tier active-key ceiling (PRD F6-7). Mirrors the backend default of
 * quota.free_max_active_keys; the backend stays authoritative and returns
 * key_limit_reached (409) with the interpolated copy. This copy only drives
 * the pre-emptive grey-out and carries no number in user-visible text.
 */
export const FREE_PLAN_MAX_ACTIVE_KEYS = 3

export interface KeyListParams {
  page?: number
  pageSize?: number
  q?: string
  status?: KeyStatus
}

export interface KeyAuditItem {
  action: string
  before: Record<string, unknown> | null
  after: Record<string, unknown> | null
  operator_id: string
  created_at: string
}

export type KeyAuditResponse = Page<KeyAuditItem>

export interface KeyModelRef {
  id: number
  model_name: string
  display_name: string
}

export interface KeyIPRule {
  cidr: string
  ip_family: number
  note?: string
}

export interface KeyRealtime {
  as_of: string
  available: boolean
  rpm_used: number | null
  rpm_limit: number
  tpm_used: number | null
  tpm_limit: number
  concurrency_used: number | null
  concurrency_limit: number
}

export interface KeyAccountQuota {
  used_today_micro: number
  daily_quota_micro: number | null
  remaining_micro: number | null
  plan_code: string
  plan_name: string
  policy_source: string
}

export interface KeyDetail {
  id: number
  key_name: string
  key_masked: string
  key_prefix: string
  status: KeyStatus
  expired: boolean
  group_code?: string
  group_ratio_bp: number | null
  group_note?: string
  model_scope_mode: ModelScopeMode
  models: KeyModelRef[]
  ip_whitelist_mode: string
  ip_allowlist: KeyIPRule[]
  expire_at: string | null
  remaining_days: number | null
  created_at: string
  last_used_at: string | null
  realtime: KeyRealtime
  account_quota: KeyAccountQuota
}

export interface UpdateKeyInput {
  model_scope_mode?: ModelScopeMode
  model_ids?: number[]
  status?: KeyStatus
}

export interface UpdateKeyResponse {
  changed: boolean
  key_name: string
  status: KeyStatus
  model_scope_mode: ModelScopeMode
  expire_at: string | null
  ip_whitelist_mode: string
  ip_allowlist: string[]
}

export interface RotateKeyResponse {
  id: number
  rotated_from_id: number
  key_name: string
  key: string
  key_prefix: string
  key_last4: string
  key_masked: string
  group_code: string
  expire_at: string | null
  warning: string
  revocation: string
}

export interface DeleteKeyResponse {
  id: number
  deleted: boolean
}

// ---------------------------------------------------------------------------
// Model catalog (GET /api/console/models)
// ---------------------------------------------------------------------------

export interface ConsoleModel {
  id: number
  model_name: string
  display_name: string
  model_type: string
  context_length: number
  protocol: string
  aliases: string[]
}

interface ConsoleModelsResponse extends Page<ConsoleModel> {
  items: ConsoleModel[]
}

// ---------------------------------------------------------------------------
// Quota apply (POST /api/console/quota-applies)
// ---------------------------------------------------------------------------

export interface CreateQuotaApplyInput {
  target_quota_micro: number
  apply_reason: string
}

export interface QuotaApplyResponse {
  apply: {
    id: number
    apply_no: string
    state: string
  }
  idempotent_replay: boolean
}

// ---------------------------------------------------------------------------
// Plain calls
// ---------------------------------------------------------------------------

export function fetchOverview(): Promise<ConsoleOverview> {
  return api.get<ConsoleOverview>('/api/console/overview')
}

function keysQuery(params: KeyListParams): string {
  const search = new URLSearchParams()
  search.set('page', String(params.page ?? 1))
  search.set('page_size', String(params.pageSize ?? 20))
  const q = params.q?.trim()
  if (q) search.set('q', q)
  if (params.status) search.set('status', params.status)
  return search.toString()
}

export function fetchKeys(params: KeyListParams): Promise<KeyListResponse> {
  return api.get<KeyListResponse>(`/api/console/keys?${keysQuery(params)}`)
}

export function fetchKeyDetail(id: string): Promise<KeyDetail> {
  return api.get<KeyDetail>(`/api/console/keys/${encodeURIComponent(id)}`)
}

export function fetchKeyAudit(id: string, page: number, pageSize = 20): Promise<KeyAuditResponse> {
  return api.get<KeyAuditResponse>(
    `/api/console/keys/${encodeURIComponent(id)}/audit?page=${page}&page_size=${pageSize}`,
  )
}

/**
 * Walks the catalog until every listed model is loaded. The wizard needs the
 * full set for the SELECTED scope; the API caps page_size at 100.
 */
export async function fetchAllModels(): Promise<ConsoleModel[]> {
  const pageSize = 100
  const out: ConsoleModel[] = []
  for (let page = 1; page <= 10; page += 1) {
    const response = await api.get<ConsoleModelsResponse>(
      `/api/console/models?page=${page}&page_size=${pageSize}`,
    )
    out.push(...response.items)
    if (response.items.length < pageSize || out.length >= response.total) break
  }
  return out
}

// ---------------------------------------------------------------------------
// Query hooks
// ---------------------------------------------------------------------------

/**
 * `poll: false` is for pages that only need the plan code (e.g. the keys page
 * decides whether the free-tier ceiling applies); they should not refresh the
 * whole overview every 15s.
 */
export function useConsoleOverview(options: { poll?: boolean } = {}) {
  return useQuery({
    queryKey: ['console', 'overview'],
    queryFn: fetchOverview,
    refetchInterval: options.poll === false ? false : 15_000,
  })
}

export function useKeys(params: KeyListParams) {
  return useQuery({
    queryKey: ['console', 'keys', 'list', params],
    queryFn: () => fetchKeys(params),
    placeholderData: keepPreviousData,
  })
}

/**
 * Active (enabled, not expired) key count for the free-tier ceiling. The
 * backend counts the same way; only the ceiling itself is not exposed, which
 * is why FREE_PLAN_MAX_ACTIVE_KEYS exists.
 */
export function useActiveKeyCount(enabled: boolean) {
  return useQuery({
    queryKey: ['console', 'keys', 'active-count'],
    enabled,
    queryFn: async () => {
      let page = 1
      let active = 0
      for (; page <= 10; page += 1) {
        const response = await fetchKeys({ status: 'ENABLED', page, pageSize: 100 })
        active += response.items.filter((item) => !item.expired).length
        if (response.items.length < 100 || page * 100 >= response.total) break
      }
      return active
    },
  })
}

export function useKeyDetail(id: string | undefined) {
  return useQuery({
    queryKey: ['console', 'keys', 'detail', id],
    queryFn: () => fetchKeyDetail(id as string),
    enabled: Boolean(id),
  })
}

export function useKeyAudit(id: string | undefined, page: number) {
  return useQuery({
    queryKey: ['console', 'keys', 'audit', id, page],
    queryFn: () => fetchKeyAudit(id as string, page),
    enabled: Boolean(id),
    placeholderData: keepPreviousData,
  })
}

export function useConsoleModels(enabled = true) {
  return useQuery({
    queryKey: ['console', 'models', 'all'],
    queryFn: fetchAllModels,
    enabled,
    staleTime: 5 * 60_000,
  })
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

function invalidateKeys(client: ReturnType<typeof useQueryClient>, id?: number | string) {
  void client.invalidateQueries({ queryKey: ['console', 'keys'] })
  void client.invalidateQueries({ queryKey: ['console', 'overview'] })
  if (id !== undefined) {
    void client.invalidateQueries({ queryKey: ['console', 'keys', 'detail', String(id)] })
  }
}

export function useCreateKeys() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateKeysInput) =>
      api.post<CreateKeysResponse>('/api/console/keys', input),
    onSuccess: () => invalidateKeys(client),
  })
}

export function useUpdateKey() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ id, input }: { id: number | string; input: UpdateKeyInput }) =>
      api.patch<UpdateKeyResponse>(`/api/console/keys/${encodeURIComponent(String(id))}`, input),
    onSuccess: (_response, variables) => invalidateKeys(client, variables.id),
  })
}

export function useRotateKey() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (id: number | string) =>
      api.post<RotateKeyResponse>(`/api/console/keys/${encodeURIComponent(String(id))}/rotate`),
    onSuccess: (_response, id) => invalidateKeys(client, id),
  })
}

export function useDeleteKey() {
  const client = useQueryClient()
  return useMutation({
    // The backend requires an explicit confirm flag in the DELETE body.
    mutationFn: (id: number | string) =>
      api.delete<DeleteKeyResponse>(`/api/console/keys/${encodeURIComponent(String(id))}`, {
        confirm: true,
      }),
    onSuccess: (_response, id) => invalidateKeys(client, id),
  })
}

export function useCreateQuotaApply() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ input, idempotencyKey }: { input: CreateQuotaApplyInput; idempotencyKey: string }) =>
      api.post<QuotaApplyResponse>('/api/console/quota-applies', input, {
        headers: { 'Idempotency-Key': idempotencyKey },
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ['console', 'quota-applies'] })
    },
  })
}
