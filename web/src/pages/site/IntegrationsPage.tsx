import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Badge, buttonVariants } from '@/components'
import { CodeBlock, OneClickImportButton, SectionHeader } from '@/components/site'
import {
  GENERIC_FALLBACK_CODE,
  GENERIC_OPENAI_FALLBACK,
  TOOL_INTEGRATIONS,
} from '@/content/toolIntegrations'
import { ROUTES } from '@/lib/constants'
import { cn } from '@/lib/cn'
import { usePageTitle } from '@/lib/usePageTitle'
import { useAuthStore } from '@/stores/auth'

export default function IntegrationsPage() {
  usePageTitle('工具集成')

  const authenticated = useAuthStore((state) => state.status) === 'authenticated'
  const [selectedKey, setSelectedKey] = useState(TOOL_INTEGRATIONS[0].key)
  const selected = TOOL_INTEGRATIONS.find((tool) => tool.key === selectedKey) ?? TOOL_INTEGRATIONS[0]

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-12 sm:px-6">
      <header className="border-b border-border pb-8">
        <Badge variant="primary" dot>
          {TOOL_INTEGRATIONS.length} 个工具 · 只换地址
        </Badge>
        <h1 className="mt-4 text-3xl font-semibold tracking-tight text-ink sm:text-4xl">工具集成</h1>
        <p className="mt-3 max-w-2xl text-sm leading-relaxed text-ink-secondary">
          已经在用某个客户端，只需要把 Base URL、API Key 与模型名换成本平台的配置。每个工具都给出最小验证请求与常见报错。
        </p>
        <div className="mt-5 flex flex-wrap gap-3">
          <Link to={ROUTES.docs} className={buttonVariants({ variant: 'secondary', size: 'sm' })}>
            查看接入文档
          </Link>
          <Link
            to={`${ROUTES.docs}?chapter=tools`}
            className={buttonVariants({ variant: 'ghost', size: 'sm' })}
          >
            文档第 9 章
          </Link>
        </div>
      </header>

      <section className="mt-8">
        <SectionHeader
          eyebrow="TOOLS"
          title="选择你正在使用的工具"
          description="五张卡片对应 V1.0 支持的五个客户端；未登录时一键导入不可点击。"
        />

        <div className="mt-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          {TOOL_INTEGRATIONS.map((tool) => {
            const active = tool.key === selected.key
            return (
              <button
                key={tool.key}
                type="button"
                data-testid="tool-card"
                aria-pressed={active}
                onClick={() => setSelectedKey(tool.key)}
                className={cn(
                  'flex h-full flex-col gap-3 rounded-xl border bg-surface p-4 text-left shadow-card transition-colors duration-fast focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
                  active ? 'border-primary ring-1 ring-primary-ring' : 'border-border hover:border-primary-ring',
                )}
              >
                <div className="flex items-start justify-between gap-2">
                  <span className="text-sm font-semibold text-ink">{tool.name}</span>
                  <Badge variant="neutral">{tool.kind}</Badge>
                </div>
                <span className="text-xs text-ink-secondary">{tool.protocols.join(' / ')}</span>
                <Badge variant={tool.oneClick ? 'success' : 'neutral'}>
                  {tool.oneClick ? '一键导入：支持' : '一键导入：手动配置'}
                </Badge>
              </button>
            )
          })}
        </div>
      </section>

      <section className="mt-10 rounded-2xl border border-border bg-surface p-5 shadow-card sm:p-7">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="text-xl font-semibold tracking-tight text-ink">{selected.name}</h2>
              <Badge variant="neutral">{selected.kind}</Badge>
              {selected.protocols.map((protocol) => (
                <Badge key={protocol} variant="info">
                  {protocol}
                </Badge>
              ))}
            </div>
            <p className="mt-2 max-w-2xl text-sm text-ink-secondary">{selected.baseUrlLayer}</p>
            <p className="mt-1 max-w-2xl text-sm text-ink-secondary">{selected.modelHint}</p>
          </div>

          <OneClickImportButton
            toolKey={selected.key}
            supported={selected.oneClick}
            authenticated={authenticated}
          />
        </div>

        <div className="mt-6 grid gap-6 lg:grid-cols-2">
          <div>
            <h3 className="text-sm font-semibold text-ink">分步配置</h3>
            <ol className="mt-2 list-decimal space-y-2 pl-5 text-sm text-ink-secondary">
              {selected.steps.map((step) => (
                <li key={step}>{step}</li>
              ))}
            </ol>
          </div>

          <div>
            <h3 className="text-sm font-semibold text-ink">需要填写的字段</h3>
            <div className="mt-2 overflow-x-auto rounded-lg border border-border">
              <table className="min-w-full border-collapse text-sm">
                <caption className="sr-only">{selected.name} 配置字段</caption>
                <tbody>
                  {selected.fields.map((field) => (
                    <tr key={field.label} className="border-t border-border first:border-t-0">
                      <th
                        scope="row"
                        className="whitespace-nowrap px-3 py-2 text-left text-xs font-medium text-ink-secondary"
                      >
                        {field.label}
                      </th>
                      <td className="px-3 py-2">
                        <code className="break-all font-mono text-xs text-ink">{field.value}</code>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>

        <div className="mt-6">
          <h3 className="text-sm font-semibold text-ink">常见报错与排查</h3>
          <ul className="mt-2 space-y-1 text-sm text-ink-secondary">
            {selected.errors.map((error) => (
              <li key={error.symptom}>
                <code className="font-mono text-xs text-ink">{error.symptom}</code>：{error.fix}
              </li>
            ))}
          </ul>
        </div>

        <div className="mt-6">
          <CodeBlock code={selected.verify.code} label={selected.verify.label} />
        </div>
      </section>

      <section className="mt-12">
        <SectionHeader
          eyebrow="FALLBACK"
          title="通用兜底"
          description="你的工具不在上述列表中时，按 OpenAI SDK 的方式接入即可。"
        />
        <div className="mt-6 grid gap-5 rounded-2xl border border-border bg-surface p-5 shadow-card lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] lg:p-7">
          <div className="space-y-3">
            <p className="text-sm leading-relaxed text-ink-secondary">{GENERIC_OPENAI_FALLBACK}</p>
            <ul className="space-y-1 text-xs text-ink-secondary">
              <li>Base URL 保留 /v1，SDK 会在此基础上拼接具体路径。</li>
              <li>模型名只使用 /v1/models 返回的 model_name。</li>
              <li>401 先核对 Key；404 先检查 Base URL 层级。</li>
            </ul>
          </div>
          <CodeBlock code={GENERIC_FALLBACK_CODE} label="Python · OpenAI SDK" />
        </div>
      </section>
    </div>
  )
}
