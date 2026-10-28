import type {
  PublicAnnouncementsResponse,
  PublicEndpointsResponse,
  PublicIntegrationLinksResponse,
  PublicModelsResponse,
  PublicPlansResponse,
} from '@/api/public'
import { adminFixtureResponses } from './admin'

/**
 * DEV-ONLY contract fixtures. These values never ship: client.ts imports this
 * module only when import.meta.env.DEV && VITE_USE_FIXTURES === '1', and the
 * build asserts the marker below is absent from dist/assets/*.js.
 *
 * Plan numbers are copied from PRD §3.2.1. Invoice flags follow D-11:
 * STANDARD / PRO / ENTERPRISE = 1, TRIAL / FREE_DEFAULT = 0.
 * Model names follow the local seed planned for 0019 (qwen-max / deepseek-chat).
 */

const models: PublicModelsResponse = {
  data: [
    {
      id: 'mdl_qwen_max',
      model_name: 'qwen-max',
      display_name: 'Qwen Max',
      model_type: 'TEXT',
      listing_state: 'LISTED',
      context_window: 32768,
      input_price_micro_per_k: 1500,
      output_price_micro_per_k: 4500,
      protocols: ['OPENAI'],
      recommend_weight: 100,
      published_at: '2026-10-01T08:00:00+08:00',
    },
    {
      id: 'mdl_deepseek_chat',
      model_name: 'deepseek-chat',
      display_name: 'DeepSeek Chat',
      model_type: 'TEXT',
      listing_state: 'LISTED',
      context_window: 65536,
      input_price_micro_per_k: 1000,
      output_price_micro_per_k: 3000,
      protocols: ['OPENAI', 'ANTHROPIC'],
      recommend_weight: 80,
      published_at: '2026-09-20T08:00:00+08:00',
    },
  ],
}

const plans: PublicPlansResponse = {
  data: [
    {
      code: 'TRIAL',
      name: '体验版',
      price_fen: 0,
      grant_recharge_micro: 0,
      grant_bonus_micro: 70000000,
      bonus_valid_days: 90,
      rpm: 60,
      tpm: 126000,
      concurrency: 22,
      daily_quota_micro: 10000000,
      model_scope_mode: 'ALL',
      invoice_supported: 0,
      sla_text: '',
      permanent: 0,
      sort_order: 0,
    },
    {
      code: 'STANDARD',
      name: '标准版',
      price_fen: 9900,
      grant_recharge_micro: 99000000,
      grant_bonus_micro: 20000000,
      bonus_valid_days: 90,
      rpm: 300,
      tpm: 630000,
      concurrency: 110,
      daily_quota_micro: 50000000,
      model_scope_mode: 'ALL',
      invoice_supported: 1,
      sla_text: '7×24 工单受理，不承诺可用性百分比',
      permanent: 1,
      sort_order: 1,
    },
    {
      code: 'PRO',
      name: '专业版',
      price_fen: 29900,
      grant_recharge_micro: 299000000,
      grant_bonus_micro: 90000000,
      bonus_valid_days: 90,
      rpm: 1000,
      tpm: 2100000,
      concurrency: 367,
      daily_quota_micro: 200000000,
      model_scope_mode: 'ALL',
      invoice_supported: 1,
      sla_text: '7×24 工单受理，不承诺可用性百分比',
      permanent: 1,
      sort_order: 2,
    },
    {
      code: 'ENTERPRISE',
      name: '企业版',
      price_fen: 99900,
      grant_recharge_micro: 999000000,
      grant_bonus_micro: 400000000,
      bonus_valid_days: 90,
      rpm: 3000,
      tpm: 6300000,
      concurrency: 1100,
      daily_quota_micro: 1000000000,
      model_scope_mode: 'ALL',
      invoice_supported: 1,
      sla_text: '7×24 工单受理，不承诺可用性百分比',
      permanent: 1,
      sort_order: 3,
    },
    {
      code: 'FREE_DEFAULT',
      name: '默认档',
      price_fen: 0,
      grant_recharge_micro: 0,
      grant_bonus_micro: 0,
      bonus_valid_days: 90,
      rpm: 60,
      tpm: 60000,
      concurrency: 5,
      daily_quota_micro: null,
      model_scope_mode: 'ALL',
      invoice_supported: 0,
      sla_text: '',
      permanent: 1,
      sort_order: 4,
    },
  ],
}

const announcements: PublicAnnouncementsResponse = {
  data: [
    {
      id: 'ann_price_20261101',
      title: '标准分组文本模型价格调整',
      body: '自生效时间起，标准分组文本模型按新价目执行。具体单价以模型价格表为准，调整前已完成预扣的请求不受影响。',
      category: 'PRICE',
      effective_at: '2026-11-01T00:00:00+08:00',
      published_at: '2026-10-20T10:00:00+08:00',
    },
  ],
}

const endpoints: PublicEndpointsResponse = {
  data: [
    {
      id: 'ep_primary',
      name: '主接入点',
      base_url: 'https://api.yunshu.dev/v1',
      line: '境内线路 · 华东',
      description: '默认接入地址，客户端 Base URL 填到 /v1 这一层。',
      role: 'PRIMARY',
      status_page_url: 'https://status.yunshu.dev',
      health_status: null,
    },
    {
      id: 'ep_backup',
      name: '灾备线路',
      base_url: 'https://backup.api.yunshu.dev/v1',
      line: '境内线路 · 华北',
      description: '主接入点故障时手工切换；切换前先在控制台确认当前线路。',
      role: 'BACKUP',
      status_page_url: 'https://status.yunshu.dev',
      health_status: null,
    },
  ],
}

const integrationLinks: PublicIntegrationLinksResponse = {
  data: [
    {
      tool_key: 'cherry-studio',
      deep_link_template:
        'cherrystudio://providers/import?baseUrl={base_url}&apiKey={api_key}&model={model}',
      enabled: true,
    },
    {
      tool_key: 'nextchat',
      deep_link_template: 'nextchat://provider/import?baseUrl={base_url}&apiKey={api_key}',
      enabled: true,
    },
  ],
}

export const fixtureResponses = {
  // The marker survives tree-shaking only if this module is actually bundled.
  __yunshu_fixture_marker__: '__YUNSHU_T18_FIXTURE__',
  // T20 operator pages share the same dev-only lookup. Its own marker travels
  // with the spread, so both T18 and T20 data disappear together.
  ...adminFixtureResponses,
  '/api/public/models': models,
  '/api/public/plans': plans,
  '/api/public/announcements': announcements,
  '/api/public/endpoints': endpoints,
  '/api/public/integration-links': integrationLinks,
} as const

export type FixtureResponses = typeof fixtureResponses
