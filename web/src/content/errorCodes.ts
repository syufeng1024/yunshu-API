export interface HttpStatusRow {
  status: number
  meaning: string
  retry: string
}

export interface RateLimitFieldRow {
  field: string
  type: string
  meaning: string
}

export interface CommonErrorRow {
  code: string
  status: number
  type: string
  retry: string
  action: string
}

/**
 * /docs chapter 6 shows the HTTP status map, the 429 envelope and the codes
 * that appear most often in support tickets. It deliberately does not restate
 * the complete registry; the server-side error registry remains authoritative.
 */
export const HTTP_STATUS_ROWS: readonly HttpStatusRow[] = [
  { status: 400, meaning: '请求参数、模型名或请求体不合法', retry: '修正请求后重发' },
  { status: 401, meaning: '密钥无效、过期或账号会话失效', retry: '核对或更换 Key' },
  { status: 402, meaning: '余额不足或账号因透支冻结', retry: '充值后重试' },
  { status: 403, meaning: 'Key 停用、模型未授权或内容安全拦截', retry: '检查授权或提交工单' },
  { status: 404, meaning: '路径不存在', retry: '检查 Base URL 的 /v1 层级' },
  { status: 409, meaning: '幂等键与请求体冲突，或首次请求仍在处理', retry: '按同键重发或换键' },
  { status: 413, meaning: '请求体超过上限', retry: '压缩或拆分请求体' },
  { status: 429, meaning: 'RPM、TPM、并发或日额度触顶', retry: '按 retry_after_seconds 等待' },
  { status: 500, meaning: '平台内部错误或价格未配置', retry: '携 request_id 联系客服' },
  { status: 502, meaning: '上游连接失败、DNS 或 TLS 失败', retry: '稍后重试；持续出现联系客服' },
  { status: 503, meaning: '上游容量不足、模型暂停服务或背压拒绝', retry: '按提示等待、换模型或改非流式' },
  { status: 504, meaning: '上游响应超时', retry: '稍后重试' },
]

export const RATE_LIMIT_FIELDS: readonly RateLimitFieldRow[] = [
  { field: 'type', type: 'string', meaning: '固定 rate_limit_exceeded' },
  { field: 'code', type: 'string', meaning: '具体触顶维度，见常见错误码表' },
  { field: 'message', type: 'string', meaning: '含当前上限与等待提示' },
  { field: 'retry_after_seconds', type: 'number', meaning: '等待秒数，到恢复时刻为止' },
  { field: 'reset_at', type: 'string', meaning: '恢复时刻，仅限流类与 402 返回' },
  { field: 'limit', type: 'number', meaning: '当前维度上限' },
  { field: 'remaining', type: 'number', meaning: '当前维度剩余量' },
  { field: 'hint', type: 'string', meaning: '可操作入口，例如申请提额' },
  { field: 'request_id', type: 'string', meaning: '排障与工单关联标识' },
]

export const COMMON_ERROR_CODES: readonly CommonErrorRow[] = [
  {
    code: 'invalid_api_key',
    status: 401,
    type: 'authentication_error',
    retry: '否',
    action: '核对 Key 是否完整、是否属于当前账号',
  },
  {
    code: 'key_disabled',
    status: 403,
    type: 'authorization_error',
    retry: '否',
    action: '在密钥管理页启用或新建 Key',
  },
  {
    code: 'model_not_found',
    status: 400,
    type: 'invalid_request_error',
    retry: '否',
    action: '从 /v1/models 返回的 model_name 中取值',
  },
  {
    code: 'model_not_available',
    status: 503,
    type: 'upstream_error',
    retry: '是（30 秒）',
    action: '换用其他模型，或等待恢复公告',
  },
  {
    code: 'insufficient_credits',
    status: 402,
    type: 'billing_error',
    retry: '否',
    action: '在充值中心补足余额',
  },
  {
    code: 'rpm_limit_exceeded',
    status: 429,
    type: 'rate_limit_exceeded',
    retry: '按时长',
    action: '等待到下一分钟；持续触顶可申请提额',
  },
  {
    code: 'tpm_limit_exceeded',
    status: 429,
    type: 'rate_limit_exceeded',
    retry: '按时长',
    action: '等待到下一分钟；降低单分钟 token 用量',
  },
  {
    code: 'concurrency_limit_exceeded',
    status: 429,
    type: 'rate_limit_exceeded',
    retry: '按时长',
    action: '按 retry_after_seconds 退避后重试',
  },
  {
    code: 'daily_quota_exceeded',
    status: 429,
    type: 'rate_limit_exceeded',
    retry: '按时长',
    action: '次日恢复或提交提额申请',
  },
  {
    code: 'unknown_endpoint',
    status: 404,
    type: 'invalid_request_error',
    retry: '否',
    action: '检查 Base URL 是否多了或少了 /v1',
  },
  {
    code: 'content_policy_violation',
    status: 403,
    type: 'invalid_request_error',
    retry: '否',
    action: '修改请求内容；对判定有异议走工单申诉',
  },
  {
    code: 'upstream_timeout',
    status: 504,
    type: 'upstream_error',
    retry: '是',
    action: '稍后重试；持续出现携 request_id 联系客服',
  },
]
