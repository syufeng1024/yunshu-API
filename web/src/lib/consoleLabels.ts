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
