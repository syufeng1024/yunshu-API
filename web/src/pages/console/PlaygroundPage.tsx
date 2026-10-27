import { useEffect, useMemo, useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import { useConsoleModels, useConsoleOverview } from '@/api/console'
import {
  Badge,
  Button,
  CopyBlock,
  ErrorState,
  Input,
  PageHeader,
  Select,
} from '@/components'
import { usePageTitle } from '@/lib/usePageTitle'

type MessageRole = 'user' | 'assistant'

interface PlaygroundMessage {
  id: string
  role: MessageRole
  content: string
}

interface Example {
  id: string
  title: string
  system: string
  prompt: string
  temperature: string
  maxTokens: string
}

/**
 * Virtual prompts for guidance (F12-2): clicking one only fills the form. It
 * never sends a request and never touches billing.
 */
const EXAMPLES: readonly Example[] = [
  {
    id: 'summary',
    title: '总结技术文档',
    system: '你是一名技术编辑，回答使用简体中文，先给结论。',
    prompt: '请把下面的技术文档总结成三条要点：\n',
    temperature: '0.3',
    maxTokens: '512',
  },
  {
    id: 'error',
    title: '排查接口错误',
    system: '你是一名后端工程师，只输出可执行的排查步骤。',
    prompt: '请解释下面这个接口错误码的可能原因，并给出排查步骤：\n',
    temperature: '0.2',
    maxTokens: '800',
  },
  {
    id: 'copy',
    title: '写产品文案',
    system: '你是一名产品文案，语气克制，不使用夸张形容词。',
    prompt: '请为「请求日志」功能写一段面向开发者的介绍：',
    temperature: '0.7',
    maxTokens: '400',
  },
]

let messageSequence = 0

function newMessage(role: MessageRole, content = ''): PlaygroundMessage {
  messageSequence += 1
  return { id: `playground-message-${messageSequence}`, role, content }
}

export default function PlaygroundPage() {
  usePageTitle('在线调试台')

  const [model, setModel] = useState('')
  const [system, setSystem] = useState('')
  const [messages, setMessages] = useState<PlaygroundMessage[]>(() => [
    newMessage('user'),
  ])
  const [temperature, setTemperature] = useState('0.7')
  const [maxTokens, setMaxTokens] = useState('1024')
  const [stream, setStream] = useState(true)

  const models = useConsoleModels()
  const overview = useConsoleOverview({ poll: false })

  useEffect(() => {
    if (!model && models.data && models.data.length > 0) {
      setModel(models.data[0].model_name)
    }
  }, [model, models.data])

  const modelOptions = useMemo(
    () => [
      { value: '', label: '请选择模型' },
      ...(models.data ?? []).map((item) => ({
        value: item.model_name,
        label: item.display_name || item.model_name,
      })),
    ],
    [models.data],
  )

  const preview = useMemo(() => {
    const payloadMessages = [
      ...(system.trim() ? [{ role: 'system', content: system }] : []),
      ...messages
        .filter((message) => message.content.trim().length > 0)
        .map((message) => ({ role: message.role, content: message.content })),
    ]
    return JSON.stringify(
      {
        model: model || null,
        messages: payloadMessages,
        temperature: temperature.trim() === '' ? null : Number(temperature),
        max_tokens: maxTokens.trim() === '' ? null : Number(maxTokens),
        stream,
      },
      null,
      2,
    )
  }, [model, system, messages, temperature, maxTokens, stream])

  const balance = overview.data?.balance
  const insufficient = balance !== undefined && balance.micro <= 0
  const overviewError = overview.isError ? errorMessage(overview.error) : null
  const overviewHint = overview.isError ? errorHint(overview.error) : null
  const modelsError = models.isError ? errorMessage(models.error) : null
  const modelsHint = models.isError ? errorHint(models.error) : null

  const applyExample = (example: Example) => {
    setSystem(example.system)
    setMessages([newMessage('user', example.prompt)])
    setTemperature(example.temperature)
    setMaxTokens(example.maxTokens)
  }

  const updateMessage = (
    id: string,
    patch: Partial<Pick<PlaygroundMessage, 'role' | 'content'>>,
  ) => {
    setMessages((current) =>
      current.map((message) =>
        message.id === id ? { ...message, ...patch } : message,
      ),
    )
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="在线调试台"
        description="选择模型、编辑参数并查看将发送的请求；执行链路尚未接入。"
      />

      <div role="note" className="rounded-xl border border-info bg-info-soft p-4 text-sm">
        <p className="font-medium text-ink">流式调用尚未接入</p>
        <p className="mt-1 text-ink-secondary">
          调试台的真实执行需要后端新增控制台代理端点（会话鉴权、扣本人额度、转发
          gatewayd），当前仓库没有该端点，发送按钮不会发起任何请求，也不会产生扣费。
        </p>
        <p className="mt-1 text-ink-secondary">
          替代方案：使用已有的 API Key 直接调用网关，接口说明见{' '}
          <a href="/docs" className="text-primary underline">
            /docs
          </a>
          。
        </p>
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-2">
          <Select
            label="模型"
            value={model}
            onChange={(event) => setModel(event.target.value)}
            options={modelOptions}
            hint="仅列出平台在售（LISTED）模型；是否可调用以下游授权为准。"
          />
          {modelsError ? (
            <ErrorState
              compact
              message={modelsError}
              hint={modelsHint}
              onRetry={() => void models.refetch()}
            />
          ) : null}
        </div>

        <div className="rounded-xl border border-border bg-surface p-4 shadow-card">
          <p className="text-xs text-ink-muted">可用余额</p>
          {overview.isLoading ? (
            <p className="mt-1 text-sm text-ink-muted">加载中…</p>
          ) : overviewError ? (
            <div className="mt-1 space-y-1">
              <p className="text-sm text-ink">--</p>
              <p className="text-xs text-danger">{overviewError}</p>
              {overviewHint ? (
                <p className="text-xs text-ink-secondary">{overviewHint}</p>
              ) : null}
              <Button
                size="sm"
                variant="secondary"
                onClick={() => void overview.refetch()}
              >
                重试
              </Button>
            </div>
          ) : balance ? (
            <div className="mt-1 space-y-0.5">
              <p className="text-sm text-ink">{balance.credits} 积分</p>
              <p className="text-xs text-ink-secondary">¥{balance.cny}</p>
            </div>
          ) : (
            <p className="mt-1 text-sm text-ink">--</p>
          )}

          {insufficient ? (
            <p
              role="alert"
              className="mt-2 rounded-lg border border-danger bg-danger-soft px-2 py-1 text-xs text-danger"
            >
              余额不足，请先
              <a href="/console/recharge" className="underline">
                充值
              </a>
              后再调试。
            </p>
          ) : null}
        </div>
      </div>

      <section
        aria-label="快捷示例"
        className="space-y-2 rounded-xl border border-border bg-surface p-4 shadow-card"
      >
        <h2 className="text-sm font-semibold text-ink">快捷示例</h2>
        <p className="text-xs text-ink-secondary">
          示例仅为引导用的虚拟 prompt。点击只填入参数，不会发送，也不会计费。
        </p>
        <div className="flex flex-wrap gap-2">
          {EXAMPLES.map((example) => (
            <Button
              key={example.id}
              variant="secondary"
              size="sm"
              onClick={() => applyExample(example)}
            >
              {example.title}
            </Button>
          ))}
        </div>
      </section>

      <section
        aria-label="参数面板"
        className="space-y-4 rounded-xl border border-border bg-surface p-4 shadow-card"
      >
        <h2 className="text-sm font-semibold text-ink">参数</h2>

        <label className="block space-y-1.5">
          <span className="block text-sm font-medium text-ink">System 提示词</span>
          <textarea
            value={system}
            onChange={(event) => setSystem(event.target.value)}
            rows={3}
            className="w-full rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            placeholder="设定助手角色与回答约束"
            aria-label="System 提示词"
          />
        </label>

        <div className="space-y-2">
          <p className="text-sm font-medium text-ink">Messages</p>
          {messages.length === 0 ? (
            <p className="text-xs text-ink-muted">尚未添加消息。</p>
          ) : null}
          {messages.map((message, index) => (
            <div key={message.id} className="space-y-2 rounded-lg border border-border p-3">
              <div className="flex flex-wrap items-end gap-2">
                <div className="w-40">
                  <Select
                    label={`消息 ${index + 1} 角色`}
                    value={message.role}
                    onChange={(event) =>
                      updateMessage(message.id, {
                        role: event.target.value as MessageRole,
                      })
                    }
                    options={[
                      { value: 'user', label: '用户' },
                      { value: 'assistant', label: '助手' },
                    ]}
                  />
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={`删除消息 ${index + 1}`}
                  onClick={() =>
                    setMessages((current) =>
                      current.filter((item) => item.id !== message.id),
                    )
                  }
                >
                  删除
                </Button>
              </div>
              <label className="block space-y-1.5">
                <span className="block text-xs text-ink-secondary">
                  消息 {index + 1} 内容
                </span>
                <textarea
                  value={message.content}
                  onChange={(event) =>
                    updateMessage(message.id, { content: event.target.value })
                  }
                  rows={3}
                  aria-label={`消息 ${index + 1} 内容`}
                  className="w-full rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                  placeholder="输入消息内容"
                />
              </label>
            </div>
          ))}
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setMessages((current) => [...current, newMessage('user')])}
          >
            添加消息
          </Button>
        </div>

        <div className="grid gap-3 sm:grid-cols-3">
          <Input
            label="temperature"
            type="number"
            min={0}
            max={2}
            step="0.1"
            value={temperature}
            onChange={(event) => setTemperature(event.target.value)}
          />
          <Input
            label="max_tokens"
            type="number"
            min={1}
            step={1}
            value={maxTokens}
            onChange={(event) => setMaxTokens(event.target.value)}
          />
          <label className="flex h-full items-center gap-2 pt-6 text-sm text-ink">
            <input
              type="checkbox"
              checked={stream}
              onChange={(event) => setStream(event.target.checked)}
              className="h-4 w-4 rounded border-border-strong text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            />
            流式输出（stream）
          </label>
        </div>
      </section>

      <section
        aria-label="发送"
        className="space-y-2 rounded-xl border border-border bg-surface p-4 shadow-card"
      >
        <div className="flex flex-wrap items-center gap-3">
          <Button disabled aria-describedby="playground-send-note">
            发送（调试台需要后端代理端点，待实现）
          </Button>
          <Badge variant="warning">未接入执行链路</Badge>
        </div>
        <p id="playground-send-note" className="text-xs text-ink-secondary">
          当前没有可用的控制台代理端点，点击不会发送请求，也不会扣费；余额不足时同样不会产生上游请求。
        </p>
      </section>

      <section aria-label="请求预览" className="space-y-2">
        <h2 className="text-sm font-semibold text-ink">请求预览</h2>
        <p className="text-xs text-ink-secondary">
          以下为将要发送的请求体，仅用于核对参数。页面不会发送。
        </p>
        <CopyBlock code={preview} language="json" label="请求预览（不会发送）" />
      </section>
    </div>
  )
}
