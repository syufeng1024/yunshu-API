/**
 * Copy constants that are scanned by CI.
 *
 * The dedicated-channel wording is intentionally narrow: "优先使用指定渠道"
 * only. Any claim that implies exclusive resources or a different SLA is
 * forbidden until Legal signs off.
 *
 * TODO(Legal): 专属渠道对外文案待法务背书；背书前不得放宽 BANNED_DEDICATED_CLAIMS。
 */

export const DedicatedCopyPublic = {
  short: '优先使用指定渠道',
  detail: '专属渠道按运营维护的账号名单匹配；名单外账号不进入该渠道的候选集。',
  degradation: '专属渠道全部不可用时，按平台既有降级路径处理，并记录回落次数供月度对账。',
  disclaimer: '「专属」不改变平台统一的 SLA 与限流规则。',
} as const

const BRAND_CORE = '云枢'
const API_TOKEN = 'API'

/**
 * Built from fragments so this file itself never contains a banned literal.
 * CI compares page copy against this list.
 */
export const BANNED_BRAND_FORMS: readonly string[] = [
  `${BRAND_CORE}${API_TOKEN}`,
  `${BRAND_CORE}${API_TOKEN.toLowerCase()}`,
  `${BRAND_CORE}_${API_TOKEN}`,
  `${BRAND_CORE} ${API_TOKEN} 中转站`,
]

export const BANNED_DEDICATED_CLAIMS: readonly string[] = [
  '独享资源',
  '保证可用性',
  '更低延迟',
]

export const BANNED_TERMS: readonly string[] = [
  ...BANNED_BRAND_FORMS,
  ...BANNED_DEDICATED_CLAIMS,
]

export function findBannedTerms(text: string): string[] {
  return BANNED_TERMS.filter((term) => text.includes(term))
}
