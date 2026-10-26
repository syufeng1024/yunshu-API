import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Badge } from '@/components'
import { CodeBlock } from '@/components/site'
import { BILLING_FAQ } from '@/content/billingFaq'
import {
  COMMON_ERROR_CODES,
  HTTP_STATUS_ROWS,
  RATE_LIMIT_FIELDS,
} from '@/content/errorCodes'
import {
  EXAMPLE_ANTHROPIC_BASE_URL,
  EXAMPLE_BASE_URL,
  TOOL_INTEGRATIONS,
} from '@/content/toolIntegrations'
import { ROUTES } from '@/lib/constants'

export interface DocChapterContext {
  exampleModel: string
  imageModel: string
}

export interface DocChapter {
  id: string
  title: string
  content: ReactNode
}

function P({ children }: { children: ReactNode }) {
  return <p className="text-sm leading-relaxed text-ink-secondary">{children}</p>
}

function H3({ children }: { children: ReactNode }) {
  return <h3 className="mt-7 text-base font-semibold text-ink">{children}</h3>
}

function DocsTable({
  caption,
  headers,
  rows,
}: {
  caption: string
  headers: readonly string[]
  rows: readonly (readonly ReactNode[])[]
}) {
  return (
    <div className="mt-4 overflow-x-auto rounded-xl border border-border">
      <table className="min-w-full border-collapse text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead className="bg-surface-muted">
          <tr>
            {headers.map((header) => (
              <th
                key={header}
                scope="col"
                className="whitespace-nowrap px-4 py-3 text-left text-xs font-semibold text-ink-secondary"
              >
                {header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, rowIndex) => (
            <tr key={rowIndex} className="border-t border-border">
              {row.map((cell, cellIndex) => (
                <td key={cellIndex} className="px-4 py-3 align-top text-ink-secondary">
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function InlineCode({ children }: { children: ReactNode }) {
  return (
    <code className="rounded bg-surface-muted px-1 py-0.5 font-mono text-[0.85em] text-ink">
      {children}
    </code>
  )
}

export function buildDocChapters(
  ctx: DocChapterContext,
  hasImageModel: boolean,
): DocChapter[] {
  const model = ctx.exampleModel
  const imageModel = ctx.imageModel

  const quickstartCurl = `curl ${EXAMPLE_BASE_URL}/chat/completions \\
  -H "Authorization: Bearer sk-xxxxxxxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${model}","messages":[{"role":"user","content":"ping"}]}'`

  const quickstartPython = `from openai import OpenAI

client = OpenAI(
    api_key="sk-xxxxxxxx",
    base_url="${EXAMPLE_BASE_URL}",
)

resp = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "ping"}],
)
print(resp.choices[0].message.content)`

  const quickstartNode = `import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "sk-xxxxxxxx",
  baseURL: "${EXAMPLE_BASE_URL}",
});

const resp = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "ping" }],
});
console.log(resp.choices[0].message.content);`

  const authCurl = `curl ${EXAMPLE_BASE_URL}/chat/completions \\
  -H "Authorization: Bearer sk-xxxxxxxx" \\
  -H "Content-Type: application/json" \\
  -H "X-Request-Id: req_0000000000000000" \\
  -d '{"model":"${model}","messages":[{"role":"user","content":"ping"}]}'`

  const anthropicCurl = `curl ${EXAMPLE_ANTHROPIC_BASE_URL}/v1/messages \\
  -H "x-api-key: sk-xxxxxxxx" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${model}","max_tokens":64,"messages":[{"role":"user","content":"ping"}]}'`

  const streamExample = `data: {"id":"req_0000000000000000","choices":[{"delta":{"content":"你"}}]}

data: {"id":"req_0000000000000000","choices":[{"delta":{"content":"好"}}]}

data: [DONE]`

  const streamErrorExample = `event: error
data: {"error":{"type":"upstream_error","code":"upstream_timeout","message":"上游响应超时，已重试 1 次","retry_after_seconds":30,"hint":"稍后重试；本次已按实际输出计费","request_id":"req_0000000000000000","partial":true,"credits_consumed":42}}`

  const imageCurl = `curl ${EXAMPLE_BASE_URL}/images/generations \\
  -H "Authorization: Bearer sk-xxxxxxxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${imageModel}","prompt":"一只在窗边看雨的橘猫","size":"<尺寸>","n":1}'`

  const billingFormula = `费用 = (输入 tokens ÷ 1000 × 输入单价) + (输出 tokens ÷ 1000 × 输出单价)

输入单价、输出单价单位：积分 / 千 token`

  const chapters: DocChapter[] = [
    {
      id: 'quickstart',
      title: '快速开始',
      content: (
        <div className="space-y-4">
          <P>
            依次完成三步：在控制台创建 Key、把 Base URL 填进客户端、发起一次请求。下文示例中的模型名取自当前已上架模型列表。
          </P>
          <ol className="list-decimal space-y-2 pl-5 text-sm text-ink-secondary">
            <li>登录控制台，在密钥管理页创建 Key。</li>
            <li>
              Base URL 填 <InlineCode>{EXAMPLE_BASE_URL}</InlineCode>（OpenAI 兼容路径保留{' '}
              <InlineCode>/v1</InlineCode>）。
            </li>
            <li>
              模型名从 <InlineCode>GET /v1/models</InlineCode> 返回的 <InlineCode>model_name</InlineCode> 中选择。
            </li>
          </ol>
          <H3>curl</H3>
          <CodeBlock code={quickstartCurl} label="curl" />
          <H3>Python</H3>
          <CodeBlock code={quickstartPython} label="python" />
          <H3>Node</H3>
          <CodeBlock code={quickstartNode} label="node" />
        </div>
      ),
    },
    {
      id: 'auth',
      title: '鉴权与请求头',
      content: (
        <div className="space-y-4">
          <P>
            OpenAI 兼容路径使用 <InlineCode>Authorization: Bearer &lt;key&gt;</InlineCode>；
            Anthropic 路径使用 <InlineCode>x-api-key</InlineCode> 与{' '}
            <InlineCode>anthropic-version</InlineCode>。两条路径不得混用认证头。
          </P>
          <DocsTable
            caption="请求头清单"
            headers={['请求头', '是否必需', '说明']}
            rows={[
              ['Authorization', 'OpenAI 路径必需', 'Bearer <key>；Key 在控制台创建。'],
              ['x-api-key', 'Anthropic 路径必需', '与 Authorization 二选一；不一致时返回 400 conflicting_auth_headers。'],
              ['anthropic-version', 'Anthropic 路径必需', '当前使用 2023-06-01。'],
              ['Content-Type', '是', 'application/json。'],
              ['X-Request-Id', '否', '客户端链路追踪；未带时由平台生成。'],
              ['Idempotency-Key', '否', '非流式重试可复用；同键不同请求体返回 409。'],
            ]}
          />
          <CodeBlock code={authCurl} label="curl" />
        </div>
      ),
    },
    {
      id: 'protocol',
      title: '协议兼容',
      content: (
        <div className="space-y-4">
          <P>
            「兼容」指接入协议，不代表厂商来源地。V1.0 上游厂商均为境内；OpenAI 兼容与 Anthropic
            兼容描述的是请求与响应格式，不是上游归属。
          </P>
          <DocsTable
            caption="两种协议的差异"
            headers={['项目', 'OpenAI 兼容', 'Anthropic']}
            rows={[
              ['聊天路径', <InlineCode>/v1/chat/completions</InlineCode>, <InlineCode>/v1/messages</InlineCode>],
              ['认证头', <InlineCode>Authorization: Bearer</InlineCode>, <InlineCode>x-api-key</InlineCode>],
              ['系统消息', '放在 messages 中，role=system', '使用顶层 system 字段'],
              ['响应正文', 'choices[].message.content', 'content[] 文本块'],
              ['流式结束', <InlineCode>data: [DONE]</InlineCode>, <InlineCode>message_stop</InlineCode>],
            ]}
          />
          <H3>Anthropic 最小请求</H3>
          <CodeBlock code={anthropicCurl} label="curl" />
        </div>
      ),
    },
    {
      id: 'streaming',
      title: '流式响应（SSE）',
      content: (
        <div className="space-y-4">
          <P>
            请求体传 <InlineCode>stream: true</InlineCode> 后，响应以 <InlineCode>text/event-stream</InlineCode>{' '}
            返回。OpenAI 兼容路径的每个分片以 <InlineCode>data:</InlineCode> 开头，正常结束标记为{' '}
            <InlineCode>data: [DONE]</InlineCode>。需要 usage 时可在请求中开启流式 usage 选项；未返回时以控制台账单为准。
          </P>
          <CodeBlock code={streamExample} label="正常流" />
          <H3>首字节后失败</H3>
          <P>
            已开始输出后上游失败，不再改 HTTP 状态码，也不再补 <InlineCode>[DONE]</InlineCode>。Anthropic
            路径返回 <InlineCode>event: error</InlineCode>，OpenAI 兼容路径返回{' '}
            <InlineCode>data: {'{"error":{...}}'}</InlineCode> 后关闭连接。错误对象与普通错误响应同构，另带{' '}
            <InlineCode>partial</InlineCode> 与 <InlineCode>credits_consumed</InlineCode>。
          </P>
          <CodeBlock code={streamErrorExample} label="错误帧" />
          <ul className="list-disc space-y-1 pl-5 text-sm text-ink-secondary">
            <li>已输出部分按实际用量计费，未输出部分不计。</li>
            <li>客户端主动断开时不发送错误帧，服务端按已输出计费。</li>
            <li>首字节前失败走普通 HTTP 错误响应，不进入流式帧。</li>
          </ul>
        </div>
      ),
    },
    ...(hasImageModel
      ? [
          {
            id: 'image',
            title: '图像生成',
            content: (
              <div className="space-y-4">
                <P>
                  图像生成使用 OpenAI 兼容路径。模型名必须是当前已上架图像模型；不在列表中的模型返回{' '}
                  <InlineCode>model_not_found</InlineCode>。
                </P>
                <DocsTable
                  caption="图像生成请求参数"
                  headers={['参数', '说明']}
                  rows={[
                    ['prompt', '提示词，不能为空。'],
                    ['size', '尺寸取值以服务端返回的支持列表为准；不支持时返回 invalid_image_size。'],
                    ['n', '生成张数；超过上限返回 invalid_image_count。'],
                  ]}
                />
                <CodeBlock code={imageCurl} label="curl" />
                <P>
                  返回体包含图像 URL 或 base64 数据。计费方式以模型价格表与账单为准；未产生图像不计费。
                </P>
              </div>
            ),
          } as DocChapter,
        ]
      : []),
    {
      id: 'errors',
      title: '错误码与限流',
      content: (
        <div className="space-y-4">
          <P>
            所有错误响应使用统一结构：<InlineCode>error.type</InlineCode>、<InlineCode>error.code</InlineCode>、
            <InlineCode>message</InlineCode>、<InlineCode>retry_after_seconds</InlineCode>、
            <InlineCode>hint</InlineCode> 与 <InlineCode>request_id</InlineCode> 六项必填。
          </P>
          <H3>HTTP 状态码</H3>
          <DocsTable
            caption="HTTP 状态码总表"
            headers={['状态码', '含义', '重试建议']}
            rows={HTTP_STATUS_ROWS.map((row) => [
              <InlineCode>{row.status}</InlineCode>,
              row.meaning,
              row.retry,
            ])}
          />
          <H3>429 字段口径</H3>
          <DocsTable
            caption="限流响应字段"
            headers={['字段', '类型', '说明']}
            rows={RATE_LIMIT_FIELDS.map((row) => [
              <InlineCode>{row.field}</InlineCode>,
              row.type,
              row.meaning,
            ])}
          />
          <H3>常见错误码</H3>
          <DocsTable
            caption="常见错误码"
            headers={['code', 'HTTP', '可重试', '处理动作']}
            rows={COMMON_ERROR_CODES.map((row) => [
              <InlineCode>{row.code}</InlineCode>,
              <InlineCode>{row.status}</InlineCode>,
              row.retry,
              row.action,
            ])}
          />
          <ul className="list-disc space-y-1 pl-5 text-sm text-ink-secondary">
            <li>4xx 先修正请求、Key 或余额，不要原样重试。</li>
            <li>429 按 retry_after_seconds 等待后重试；不要通过并发补偿。</li>
            <li>上游 5xx 与超时可重试一次；上游限流不在此列。</li>
          </ul>
          <p className="text-xs text-ink-muted">
            完整错误码注册表由服务端维护；本章只列排障最常见的条目，避免两处码表漂移。
          </p>
        </div>
      ),
    },
    {
      id: 'billing',
      title: '积分与账单',
      content: (
        <div className="space-y-4">
          <P>
            金额以整数微积分运算，请求级只做一次舍入；展示层将微积分换算为积分与人民币。充值、扣费与账单使用同一换算比例。
          </P>
          <H3>文本计费公式</H3>
          <CodeBlock code={billingFormula} label="计费公式" />
          <P>
            账单与用量记录在{' '}
            <Link to={ROUTES.consoleUsage} className="font-medium text-primary hover:text-primary-hover">
              控制台 · 用量与账单
            </Link>{' '}
            查看；充值入口在{' '}
            <Link to={ROUTES.consoleRecharge} className="font-medium text-primary hover:text-primary-hover">
              控制台 · 充值中心
            </Link>
            。
          </P>
          <H3>常见问题</H3>
          <dl className="space-y-4">
            {BILLING_FAQ.map((item) => (
              <div key={item.id} className="rounded-xl border border-border bg-surface p-4">
                <dt className="text-sm font-medium text-ink">{item.question}</dt>
                <dd className="mt-1 text-sm leading-relaxed text-ink-secondary">{item.answer}</dd>
              </div>
            ))}
          </dl>
        </div>
      ),
    },
    {
      id: 'channels',
      title: '新增渠道与路径',
      content: (
        <div className="space-y-4">
          <P>
            新增上游是配置动作，不改网关代码，也不发前端版本。运营在后台按以下顺序完成，并在灰度通过后提升流量权重。
          </P>
          <ol className="list-decimal space-y-3 pl-5 text-sm text-ink-secondary">
            <li>
              <span className="font-medium text-ink">创建渠道</span>：在{' '}
              <InlineCode>/admin/channels</InlineCode> 填写上游地址、密钥、超时与并发上限。
            </li>
            <li>
              <span className="font-medium text-ink">模型映射</span>：在{' '}
              <InlineCode>/admin/models</InlineCode> 把上游模型名映射到平台模型与别名。
            </li>
            <li>
              <span className="font-medium text-ink">路由权重</span>：为候选渠道设置权重与灾备顺序；主渠道故障时按顺序切换。
            </li>
            <li>
              <span className="font-medium text-ink">灰度验证</span>：先用小流量开启，观察请求日志中的错误码、耗时与重试次数，再提升权重。
            </li>
          </ol>
          <P>
            V1.0 不接入境外上游。模型处于仅目录状态时，对外接口不返回该模型；已上架状态才进入价格表与文档示例。
          </P>
        </div>
      ),
    },
    {
      id: 'tools',
      title: '工具集成',
      content: (
        <div className="space-y-5">
          <P>
            下面五个客户端的配置说明与{' '}
            <Link to={ROUTES.integrations} className="font-medium text-primary hover:text-primary-hover">
              工具集成页
            </Link>{' '}
            共用同一份内容源。Base URL 是否带 <InlineCode>/v1</InlineCode> 是最高频的报错来源，逐项按表填写。
          </P>
          {TOOL_INTEGRATIONS.map((tool) => (
            <section key={tool.key} className="rounded-xl border border-border bg-surface p-5">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="text-sm font-semibold text-ink">{tool.name}</h3>
                <Badge variant="neutral">{tool.kind}</Badge>
                <Badge variant="info">{tool.protocols.join(' / ')}</Badge>
                <Badge variant={tool.oneClick ? 'success' : 'neutral'}>
                  {tool.oneClick ? '支持一键导入' : '手动配置'}
                </Badge>
              </div>
              <p className="mt-2 text-sm text-ink-secondary">{tool.baseUrlLayer}</p>
              <p className="mt-1 text-sm text-ink-secondary">{tool.modelHint}</p>
              <ol className="mt-3 list-decimal space-y-1 pl-5 text-sm text-ink-secondary">
                {tool.steps.map((step) => (
                  <li key={step}>{step}</li>
                ))}
              </ol>
              <ul className="mt-3 space-y-1 text-xs text-ink-secondary">
                {tool.errors.map((error) => (
                  <li key={error.symptom}>
                    <span className="font-mono text-ink">{error.symptom}</span>：{error.fix}
                  </li>
                ))}
              </ul>
              <div className="mt-4">
                <CodeBlock code={tool.verify.code} label={tool.verify.label} />
              </div>
            </section>
          ))}
        </div>
      ),
    },
  ]

  return chapters
}
