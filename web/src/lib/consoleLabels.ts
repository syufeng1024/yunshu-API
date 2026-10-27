/**
 * Display labels for console enums. The backend already omits the internal
 * group (visibleGroup returns ""), so an unknown or empty code is rendered as
 * absent rather than guessed.
 */

const KEY_GROUP_LABEL: Record<string, string> = {
  standard: '标准',
  contract: '合约',
}

export function groupLabel(code: string | null | undefined): string | null {
  if (!code) return null
  return KEY_GROUP_LABEL[code] ?? code
}

const AUDIT_ACTION_LABEL: Record<string, string> = {
  CREATE: '创建',
  RENAME: '重命名',
  MODEL_SCOPE_CHANGE: '模型权限变更',
  IP_RULE_CHANGE: 'IP 白名单变更',
  EXPIRE_CHANGE: '有效期变更',
  ENABLE: '启用',
  DISABLE: '停用',
  ROTATE: '轮换',
  DELETE: '删除',
}

export function auditActionLabel(action: string): string {
  return AUDIT_ACTION_LABEL[action] ?? action
}

const AUDIT_FIELD_LABEL: Record<string, string> = {
  key_name: '名称',
  key_prefix: '前缀',
  key_last4: '后四位',
  status: '状态',
  model_scope_mode: '模型范围',
  model_ids: '模型 ID',
  group_code: '分组',
  expire_at: '到期时间',
  ip_whitelist_mode: 'IP 模式',
  ip_allowlist: 'IP 白名单',
  revoked_at: '失效时间',
  new_key_id: '新密钥 ID',
  new_key_prefix: '新密钥前缀',
  new_key_last4: '新密钥后四位',
  deleted_at: '删除时间',
}

export function auditFieldLabel(field: string): string {
  return AUDIT_FIELD_LABEL[field] ?? field
}

const ORDER_STATE_LABEL: Record<string, string> = {
  PENDING: '待支付',
  PAID: '已支付',
  CLOSED: '已关闭',
  REFUNDING: '退款中',
  REFUNDED: '已退款',
  PARTIAL_REFUNDED: '部分退款',
}

export function orderStateLabel(state: string): string {
  return ORDER_STATE_LABEL[state] ?? state
}

const REFUND_STATE_LABEL: Record<string, string> = {
  APPLIED: '已申请',
  APPROVED: '已通过',
  REJECTED: '已驳回',
  PAYING: '退款中',
  PAID: '已退款',
  FAILED: '退款失败',
}

export function refundStateLabel(state: string): string {
  return REFUND_STATE_LABEL[state] ?? state
}

const INVOICE_STATE_LABEL: Record<string, string> = {
  APPLIED: '已申请',
  APPROVED: '已通过',
  ISSUED: '已开具',
  REJECTED: '已驳回',
  VOIDED: '已作废',
}

export function invoiceStateLabel(state: string): string {
  return INVOICE_STATE_LABEL[state] ?? state
}

const MODEL_TYPE_LABEL: Record<string, string> = {
  TEXT: '文本',
  IMAGE: '图像',
  EMBEDDING: '向量',
}

export function modelTypeLabel(type: string): string {
  return MODEL_TYPE_LABEL[type] ?? type
}

/**
 * Protocol codes come from t_protocol seeds; the API exposes only the code, so
 * an unknown (newly added) protocol falls back to the code instead of being
 * guessed.
 */
const PROTOCOL_LABEL: Record<string, string> = {
  BAILIAN: '百炼',
  OPENAI_COMPAT: 'OpenAI 兼容',
  ANTHROPIC_COMPAT: 'Anthropic 兼容',
}

export function protocolLabel(code: string): string {
  return PROTOCOL_LABEL[code] ?? code
}

const PAY_CHANNEL_LABEL: Record<string, string> = {
  WECHAT: '微信支付',
  ALIPAY: '支付宝',
}

export function payChannelLabel(channel: string): string {
  return PAY_CHANNEL_LABEL[channel] ?? channel
}

const DIMENSION_LABEL: Record<string, string> = {
  model: '按模型',
  channel: '按渠道',
  day: '按日',
}

export function dimensionLabel(dimension: string): string {
  return DIMENSION_LABEL[dimension] ?? dimension
}
