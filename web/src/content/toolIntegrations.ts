export type ToolKind = 'IDE' | '客户端' | 'SDK'

export interface ToolField {
  label: string
  value: string
}

export interface ToolError {
  symptom: string
  fix: string
}

export interface ToolVerification {
  label: string
  code: string
}

export interface ToolIntegration {
  key: string
  name: string
  kind: ToolKind
  protocols: readonly string[]
  /** Whether this tool has a backend-configured deep link (F4-3). */
  oneClick: boolean
  baseUrlLayer: string
  modelHint: string
  steps: readonly string[]
  fields: readonly ToolField[]
  errors: readonly ToolError[]
  verify: ToolVerification
}

export const EXAMPLE_BASE_URL = 'https://api.yunshu.dev/v1'
export const EXAMPLE_ANTHROPIC_BASE_URL = 'https://api.yunshu.dev'

const OPENAI_CHAT_CURL = `curl ${EXAMPLE_BASE_URL}/chat/completions \\
  -H "Authorization: Bearer sk-xxxxxxxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"<模型名>","messages":[{"role":"user","content":"ping"}]}'`

const ANTHROPIC_MESSAGES_CURL = `curl ${EXAMPLE_ANTHROPIC_BASE_URL}/v1/messages \\
  -H "x-api-key: sk-xxxxxxxx" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"<模型名>","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}'`

/**
 * Single content source shared by /integrations and /docs chapter 9 (F4-5).
 * Only the five tools in PRD §4.5 are listed; do not add a sixth card.
 */
export const TOOL_INTEGRATIONS: readonly ToolIntegration[] = [
  {
    key: 'cursor',
    name: 'Cursor',
    kind: 'IDE',
    protocols: ['OpenAI'],
    oneClick: false,
    baseUrlLayer: 'Base URL 填 https://api.yunshu.dev/v1（保留 /v1）。',
    modelHint: '模型名填 /v1/models 返回的 model_name，不要自行拼接厂商前缀。',
    steps: [
      '打开 Cursor Settings → Models，展开 OpenAI API Key 配置。',
      '启用 Override OpenAI Base URL，填入带 /v1 的接入地址。',
      'API Key 填控制台创建的密钥，模型名从 /v1/models 返回列表中选择。',
      '新开一个会话发送一条短消息，确认返回符合预期。',
    ],
    fields: [
      { label: 'Base URL', value: EXAMPLE_BASE_URL },
      { label: 'API Key', value: 'sk-xxxxxxxx' },
      { label: 'Model', value: '<模型名>' },
    ],
    errors: [
      { symptom: '404 unknown_endpoint', fix: 'Base URL 多了或少了一层 /v1。' },
      { symptom: '401 invalid_api_key', fix: 'Key 复制不完整、已停用或不属于当前账号。' },
      { symptom: '400 model_not_found', fix: '模型名不在 /v1/models 返回的列表中。' },
    ],
    verify: { label: '最小验证请求', code: OPENAI_CHAT_CURL },
  },
  {
    key: 'claude-code',
    name: 'Claude Code',
    kind: '客户端',
    protocols: ['Anthropic'],
    oneClick: false,
    baseUrlLayer:
      'ANTHROPIC_BASE_URL 填 https://api.yunshu.dev（不带 /v1，客户端会自行拼接 /v1/messages）。',
    modelHint: '模型名填 /v1/models 返回的 model_name；该列表只含当前已上架模型。',
    steps: [
      '在终端设置 ANTHROPIC_BASE_URL，值不带 /v1。',
      '设置 ANTHROPIC_AUTH_TOKEN 为控制台创建的密钥。',
      '启动 Claude Code，选择从 /v1/models 获取的模型名。',
      '发送一条短消息，确认请求进入控制台日志。',
    ],
    fields: [
      { label: 'ANTHROPIC_BASE_URL', value: EXAMPLE_ANTHROPIC_BASE_URL },
      { label: 'ANTHROPIC_AUTH_TOKEN', value: 'sk-xxxxxxxx' },
      { label: 'Model', value: '<模型名>' },
    ],
    errors: [
      {
        symptom: '404 unknown_endpoint',
        fix: 'Base URL 误填了 /v1，客户端再拼一次会变成 /v1/v1/messages。',
      },
      { symptom: '401 invalid_api_key', fix: 'AUTH_TOKEN 为空或复制时带入了多余空格。' },
      { symptom: '400 model_not_found', fix: '模型名不在 /v1/models 返回的列表中。' },
    ],
    verify: { label: '最小验证请求', code: ANTHROPIC_MESSAGES_CURL },
  },
  {
    key: 'cherry-studio',
    name: 'Cherry Studio',
    kind: '客户端',
    protocols: ['OpenAI'],
    oneClick: true,
    baseUrlLayer: '服务商地址填 https://api.yunshu.dev/v1（保留 /v1）。',
    modelHint: '添加模型时直接粘贴 /v1/models 返回的 model_name。',
    steps: [
      '在设置 → 模型服务商中新增一个 OpenAI 兼容服务商。',
      '服务商地址填带 /v1 的接入地址，API Key 填控制台创建的密钥。',
      '点击「获取模型列表」，勾选需要使用的模型。',
      '回到对话页选择模型并发送一条短消息。',
    ],
    fields: [
      { label: '服务商地址', value: EXAMPLE_BASE_URL },
      { label: 'API Key', value: 'sk-xxxxxxxx' },
      { label: '模型', value: '<模型名>' },
    ],
    errors: [
      { symptom: '404 unknown_endpoint', fix: '服务商地址少了 /v1，或重复填了 /v1。' },
      { symptom: '401 invalid_api_key', fix: 'Key 复制不完整或已停用。' },
      { symptom: '400 model_not_found', fix: '模型名与 /v1/models 返回的不一致。' },
    ],
    verify: { label: '最小验证请求', code: OPENAI_CHAT_CURL },
  },
  {
    key: 'nextchat',
    name: 'NextChat',
    kind: '客户端',
    protocols: ['OpenAI'],
    oneClick: true,
    baseUrlLayer: '接口地址填 https://api.yunshu.dev/v1（保留 /v1）。',
    modelHint: '自定义模型名从 /v1/models 返回的 model_name 中取。',
    steps: [
      '打开设置 → 自定义接口，接口类型选择 OpenAI。',
      '接口地址填带 /v1 的接入地址，API Key 填控制台创建的密钥。',
      '在自定义模型中填入 /v1/models 返回的 model_name。',
      '返回对话页，选择该模型并发送一条短消息。',
    ],
    fields: [
      { label: '接口地址', value: EXAMPLE_BASE_URL },
      { label: 'API Key', value: 'sk-xxxxxxxx' },
      { label: '自定义模型', value: '<模型名>' },
    ],
    errors: [
      { symptom: '404 unknown_endpoint', fix: '接口地址少了 /v1。' },
      { symptom: '401 invalid_api_key', fix: 'Key 复制不完整或已停用。' },
      { symptom: '400 model_not_found', fix: '自定义模型名不在 /v1/models 返回的列表中。' },
    ],
    verify: { label: '最小验证请求', code: OPENAI_CHAT_CURL },
  },
  {
    key: 'openai-sdk',
    name: 'OpenAI SDK',
    kind: 'SDK',
    protocols: ['OpenAI'],
    oneClick: false,
    baseUrlLayer: 'base_url 填 https://api.yunshu.dev/v1（SDK 会在此基础上拼接 /chat/completions）。',
    modelHint: 'model 参数填 /v1/models 返回的 model_name。',
    steps: [
      '安装官方 OpenAI SDK。',
      '把 base_url 指向接入地址，保留 /v1。',
      'api_key 使用控制台创建的密钥；model 从 /v1/models 返回列表中选择。',
      '发送一次非流式请求，确认返回结构与 OpenAI 兼容。',
    ],
    fields: [
      { label: 'base_url', value: EXAMPLE_BASE_URL },
      { label: 'api_key', value: 'sk-xxxxxxxx' },
      { label: 'model', value: '<模型名>' },
    ],
    errors: [
      { symptom: '404 unknown_endpoint', fix: 'base_url 少了 /v1，SDK 不会替你补全。' },
      { symptom: '401 invalid_api_key', fix: 'Key 复制不完整或已停用。' },
      { symptom: '400 model_not_found', fix: 'model 参数与 /v1/models 返回的不一致。' },
    ],
    verify: {
      label: '最小验证请求（Python）',
      code: `from openai import OpenAI

client = OpenAI(
    api_key="sk-xxxxxxxx",
    base_url="${EXAMPLE_BASE_URL}",
)

resp = client.chat.completions.create(
    model="<模型名>",
    messages=[{"role": "user", "content": "ping"}],
)
print(resp.choices[0].message.content)`,
    },
  },
]

export const GENERIC_OPENAI_FALLBACK =
  '任何兼容 OpenAI 协议的客户端都可以按同一方式接入：Base URL 填 https://api.yunshu.dev/v1，API Key 使用控制台创建的密钥，模型名取 /v1/models 返回的 model_name。'

export const GENERIC_FALLBACK_CODE = `from openai import OpenAI

client = OpenAI(
    api_key="sk-xxxxxxxx",
    base_url="${EXAMPLE_BASE_URL}",
)

resp = client.chat.completions.create(
    model="<模型名>",
    messages=[{"role": "user", "content": "ping"}],
)
print(resp.choices[0].message.content)`

export function toolByKey(key: string): ToolIntegration | undefined {
  return TOOL_INTEGRATIONS.find((tool) => tool.key === key)
}
