import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { ApiError, errorHint, errorMessage } from '@/api/client'
import {
  playgroundChat,
  playgroundCreditsRemaining,
  useConsoleModels,
  useConsoleOverview,
  useKeys,
} from '@/api/console'
import type { KeyListParams, KeyListItem } from '@/api/console'
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

type CodeLanguage = 'curl' | 'python' | 'node'
type ReplyState = 'streaming' | 'done' | 'aborted'

interface AssistantReply {
  id: string
  content: string
  state: ReplyState
}

/** The selected key list: one page is enough for the debug console. */
const KEY_LIST_PARAMS: KeyListParams = { page: 1, pageSize: 50 }

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

const CODE_LANGUAGE_OPTIONS = [
  { value: 'curl', label: 'curl' },
  { value: 'python', label: 'Python' },
  { value: 'node', label: 'Node' },
]

let messageSequence = 0

function newMessage(role: MessageRole, content = ''): PlaygroundMessage {
  messageSequence += 1
  return { id: `playground-message-${messageSequence}`, role, content }
}

function isAbortError(error: unknown): boolean {
  return (
    typeof error === 'object' &&
    error !== null &&
    (error as { name?: unknown }).name === 'AbortError'
  )
}

function isUsableKey(key: KeyListItem): boolean {
  return key.status === 'ENABLED' && !key.expired
}

/**
 * Builds the sample snippet for direct gateway calls. The key is replaced by
 * the server-issued mask; no code path in this page ever holds a plaintext key.
 */
function buildSnippet(
  language: CodeLanguage,
  endpoint: string,
  maskedKey: string,
  payload: Record<string, unknown>,
): string {
  const json = JSON.stringify(payload, null, 2)
  if (language === 'curl') {
    // Single-quoted shell strings cannot contain a bare quote.
    const shellJson = json.replace(/'/g, `'\\''`)
    return [
      `curl -N ${endpoint} \\`,
      `  -H "Authorization: Bearer ${maskedKey}" \\`,
      `  -H "Content-Type: application/json" \\`,
      `  -d '${shellJson}'`,
    ].join('\n')
  }
  if (language === 'python') {
    // json.loads keeps the payload valid when messages contain quotes or
    // newlines; the quoted literal is produced by JSON.stringify.
    return [
      'import json',
      'import requests',
      '',
      `payload = json.loads(${JSON.stringify(json)})`,
      '',
      'response = requests.post(',
      `    "${endpoint}",`,
      '    headers={',
      `        "Authorization": "Bearer ${maskedKey}",`,
      '        "Content-Type": "application/json",',
      '    },',
      '    data=json.dumps(payload).encode("utf-8"),',
      '    stream=True,',
      ')',
      'for line in response.iter_lines(decode_unicode=True):',
      '    if line:',
      '        print(line)',
    ].join('\n')
  }
  return [
    `const payload = ${json}`,
    '',
    `const response = await fetch("${endpoint}", {`,
    '  method: "POST",',
    '  headers: {',
    `    Authorization: "Bearer ${maskedKey}",`,
    '    "Content-Type": "application/json",',
    '  },',
    '  body: JSON.stringify(payload),',
    '})',
    '',
    'const reader = response.body.getReader()',
    'const decoder = new TextDecoder()',
    'for (;;) {',
    '  const { done, value } = await reader.read()',
    '  if (done) break',
    '  process.stdout.write(decoder.decode(value, { stream: true }))',
    '}',
  ].join('\n')
}

function apiBaseUrl(): string {
  const configured = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/+$/, '')
  if (configured) return configured
  return typeof window === 'undefined' ? 'https://api.example.com' : window.location.origin
}

export default function PlaygroundPage() {
  usePageTitle('在线调试台')

  const queryClient = useQueryClient()

  const [model, setModel] = useState('')
  const [system, setSystem] = useState('')
  const [messages, setMessages] = useState<PlaygroundMessage[]>(() => [
    newMessage('user'),
  ])
  const [temperature, setTemperature] = useState('0.7')
  const [maxTokens, setMaxTokens] = useState('1024')
  const [stream, setStream] = useState(true)

  const [keyId, setKeyId] = useState<number | null>(null)
  const [reply, setReply] = useState<AssistantReply | null>(null)
  const [pending, setPending] = useState(false)
  const [sendError, setSendError] = useState<string | null>(null)
  const [sendHint, setSendHint] = useState<string | null>(null)
  const [insufficientError, setInsufficientError] = useState(false)
  const [creditsUnknown, setCreditsUnknown] = useState(false)
  const [lastRemaining, setLastRemaining] = useState<number | null>(null)
  const [codeLanguage, setCodeLanguage] = useState<CodeLanguage>('curl')
  const abortRef = useRef<AbortController | null>(null)

  const models = useConsoleModels()
  const overview = useConsoleOverview({ poll: false })
  const keys = useKeys(KEY_LIST_PARAMS)

  useEffect(() => {
    if (!model && models.data && models.data.length > 0) {
      setModel(models.data[0].model_name)
    }
  }, [model, models.data])

  const keyItems = useMemo(() => keys.data?.items ?? [], [keys.data])
  const usableKeys = useMemo(() => keyItems.filter(isUsableKey), [keyItems])
  const selectedKey = useMemo(
    () => keyItems.find((item) => item.id === keyId) ?? null,
    [keyItems, keyId],
  )

  // Pick the first usable key once the list arrives; keep the user's choice as
  // long as it is still present.
  useEffect(() => {
    if (keyId !== null && keyItems.some((item) => item.id === keyId)) return
    setKeyId(usableKeys[0]?.id ?? null)
  }, [keyId, keyItems, usableKeys])

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

  const keyOptions = useMemo(
    () =>
      keyItems.length === 0
        ? [{ value: '', label: keys.isLoading ? '加载中…' : '暂无密钥', disabled: true }]
        : keyItems.map((item) => ({
            value: String(item.id),
            // Name + mask only: the full key never leaves the server.
            label: `${item.key_name} · ${item.key_masked}`,
            disabled: !isUsableKey(item),
          })),
    [keyItems, keys.isLoading],
  )

  const payloadMessages = useMemo(
    () => [
      ...(system.trim() ? [{ role: 'system', content: system }] : []),
      ...messages
        .filter((message) => message.content.trim().length > 0)
        .map((message) => ({ role: message.role, content: message.content })),
    ],
    [system, messages],
  )

  /**
   * One construction used by the preview, the request and the generated code.
   * `key_id` is deliberately absent: it belongs to the outer envelope only.
   */
  const requestBody = useMemo<Record<string, unknown>>(
    () => ({
      model: model || null,
      messages: payloadMessages,
      temperature: temperature.trim() === '' ? null : Number(temperature),
      max_tokens: maxTokens.trim() === '' ? null : Number(maxTokens),
      stream,
    }),
    [model, payloadMessages, temperature, maxTokens, stream],
  )

  const preview = useMemo(() => JSON.stringify(requestBody, null, 2), [requestBody])

  const balance = overview.data?.balance
  const balanceMicro = balance?.micro
  const insufficient = (balance !== undefined && balance.micro <= 0) || insufficientError

  // A refetch after a recharge must clear the sticky 402 banner.
  useEffect(() => {
    if (balanceMicro !== undefined && balanceMicro > 0) setInsufficientError(false)
  }, [balanceMicro])

  const overviewError = overview.isError ? errorMessage(overview.error) : null
  const overviewHint = overview.isError ? errorHint(overview.error) : null
  const modelsError = models.isError ? errorMessage(models.error) : null
  const modelsHint = models.isError ? errorHint(models.error) : null
  const keysError = keys.isError ? errorMessage(keys.error) : null
  const keysHint = keys.isError ? errorHint(keys.error) : null

  const canSend =
    !pending &&
    selectedKey !== null &&
    isUsableKey(selectedKey) &&
    !insufficient &&
    model.trim() !== ''

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

  const handleStop = () => {
    abortRef.current?.abort()
  }

  // Abort on unmount so a half-read stream cannot keep the pre-deduction open.
  useEffect(() => {
    return () => {
      abortRef.current?.abort()
    }
  }, [])

  const handleSend = async () => {
    if (pending) return
    setSendError(null)
    setSendHint(null)
    setInsufficientError(false)
    setCreditsUnknown(false)

    if (selectedKey === null || !isUsableKey(selectedKey)) {
      setSendError('请先选择可用的密钥。')
      setSendHint('在密钥管理页创建或启用一个密钥后再发送。')
      return
    }
    if (insufficient) {
      setInsufficientError(true)
      return
    }
    if (!requestBody.model) {
      setSendError('请先选择模型。')
      return
    }

    // Snapshot before appending the placeholder so the outgoing body is the
    // conversation the user actually wrote.
    const body = requestBody
    const key = selectedKey
    const assistantId = newMessage('assistant').id
    const controller = new AbortController()
    abortRef.current = controller

    setPending(true)
    setMessages((current) => [...current, { id: assistantId, role: 'assistant', content: '' }])
    setReply({ id: assistantId, content: '', state: 'streaming' })

    try {
      const result = await playgroundChat({
        keyId: key.id,
        body,
        signal: controller.signal,
        onDelta: (text) => {
          if (!text) return
          setMessages((current) =>
            current.map((message) =>
              message.id === assistantId
                ? { ...message, content: message.content + text }
                : message,
            ),
          )
          setReply((current) =>
            current && current.id === assistantId
              ? { ...current, content: current.content + text }
              : current,
          )
        },
        onDone: () => {
          setReply((current) =>
            current && current.id === assistantId ? { ...current, state: 'done' } : current,
          )
        },
      })

      // Balance comes from the response header or the overview endpoint only.
      if (result.creditsRemaining !== null) {
        setLastRemaining(result.creditsRemaining)
      } else {
        setCreditsUnknown(true)
      }
      setReply((current) =>
        current && current.id === assistantId ? { ...current, state: 'done' } : current,
      )
      void queryClient.invalidateQueries({ queryKey: ['console', 'overview'] })
    } catch (error) {
      if (isAbortError(error)) {
        // Keep what already arrived and label the turn instead of discarding it.
        setReply((current) =>
          current && current.id === assistantId
            ? { ...current, state: 'aborted' }
            : current,
        )
        return
      }

      setReply((current) =>
        current && current.id === assistantId ? { ...current, state: 'done' } : current,
      )
      // Drop an assistant bubble that never received a character.
      setMessages((current) =>
        current.filter((message) => message.id !== assistantId || message.content !== ''),
      )

      const remaining = playgroundCreditsRemaining(error)
      if (remaining !== null) setLastRemaining(remaining)

      if (error instanceof ApiError) {
        if (error.status === 402 || error.code === 'insufficient_credits') {
          setInsufficientError(true)
          void queryClient.invalidateQueries({ queryKey: ['console', 'overview'] })
        }
        setSendError(error.message)
        setSendHint(error.hint || null)
      } else {
        setSendError(errorMessage(error))
        setSendHint(errorHint(error))
      }
    } finally {
      setPending(false)
      abortRef.current = null
    }
  }

  const maskedKey = selectedKey?.key_masked ?? 'sk-****'
  const codeSnippet = useMemo(
    () => buildSnippet(codeLanguage, `${apiBaseUrl()}/v1/chat/completions`, maskedKey, requestBody),
    [codeLanguage, maskedKey, requestBody],
  )

  return (
    <div className="space-y-5">
      <PageHeader
        title="在线调试台"
        description="选择密钥与模型，编辑参数后发送；响应按真实扣费链路流式返回。"
      />

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-2">
          <Select
            label="密钥"
            value={keyId !== null ? String(keyId) : ''}
            onChange={(event) => setKeyId(event.target.value ? Number(event.target.value) : null)}
            options={keyOptions}
            hint="只显示密钥名称与掩码；完整密钥不会出现在浏览器中。"
          />
          {keysError ? (
            <ErrorState
              compact
              message={keysError}
              hint={keysHint}
              onRetry={() => void keys.refetch()}
            />
          ) : null}
          {!keys.isLoading && !keys.isError && usableKeys.length === 0 ? (
            <p role="alert" className="text-xs text-danger">
              请先在
              <a href="/console/keys" className="underline">
                密钥管理页
              </a>
              创建密钥。
            </p>
          ) : null}
        </div>

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

        <div className="rounded-xl border border-border bg-surface p-4 shadow-card sm:col-span-2">
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

          {lastRemaining !== null ? (
            <p className="mt-2 text-xs text-ink-secondary">
              本次调用后余额：{lastRemaining} 积分（X-Credits-Remaining）
            </p>
          ) : null}
          {creditsUnknown ? (
            <p className="mt-2 text-xs text-ink-secondary">本次消耗以用量页为准。</p>
          ) : null}

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
          <Button onClick={() => void handleSend()} disabled={!canSend}>
            {pending ? '生成中…' : '发送'}
          </Button>
          {pending ? (
            <Button variant="secondary" onClick={handleStop}>
              停止
            </Button>
          ) : null}
        </div>
        <p id="playground-send-note" className="text-xs text-ink-secondary">
          调试台调用走真实扣费，每次发送按所选密钥的计费规则扣除账户余额。
        </p>
        {sendError ? (
          <p role="alert" className="text-sm text-danger">
            {sendError}
          </p>
        ) : null}
        {sendHint ? <p className="text-xs text-ink-secondary">{sendHint}</p> : null}
      </section>

      <section
        aria-label="响应"
        className="space-y-2 rounded-xl border border-border bg-surface p-4 shadow-card"
      >
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-sm font-semibold text-ink">响应</h2>
          {reply?.state === 'aborted' ? <Badge variant="neutral">已中止</Badge> : null}
        </div>
        {reply ? (
          <p className="whitespace-pre-wrap text-sm text-ink">
            <span data-testid="playground-reply-content">{reply.content}</span>
            {reply.state === 'streaming' ? (
              <span aria-hidden="true" className="animate-pulse">
                ▍
              </span>
            ) : null}
          </p>
        ) : (
          <p className="text-xs text-ink-muted">发送后在此显示模型输出。</p>
        )}
        {reply?.state === 'aborted' ? (
          <p className="text-xs text-ink-secondary">已保留中止前收到的内容。</p>
        ) : null}
      </section>

      <section aria-label="请求预览" className="space-y-2">
        <h2 className="text-sm font-semibold text-ink">请求预览</h2>
        <p className="text-xs text-ink-secondary">
          以下为将要发送的请求体，仅用于核对参数。
        </p>
        <CopyBlock code={preview} language="json" label="请求预览" />
      </section>

      <section aria-label="调用代码" className="space-y-2">
        <h2 className="text-sm font-semibold text-ink">调用代码</h2>
        <p className="text-xs text-ink-secondary">
          密钥位置使用掩码占位，复制后请替换为你自己的完整密钥。
        </p>
        <div className="w-48">
          <Select
            label="调用代码语言"
            value={codeLanguage}
            onChange={(event) => setCodeLanguage(event.target.value as CodeLanguage)}
            options={CODE_LANGUAGE_OPTIONS}
          />
        </div>
        <CopyBlock
          code={codeSnippet}
          language={codeLanguage}
          label={`调用代码（${codeLanguage}）`}
        />
      </section>
    </div>
  )
}
