import { useEffect, useMemo, useState } from 'react'
import { ApiError, errorHint, errorMessage } from '@/api/client'
import {
  KEY_BATCH_MAX,
  KEY_BATCH_MIN,
  useConsoleModels,
  useCreateKeys,
} from '@/api/console'
import type { CreatedKey, ModelScopeMode } from '@/api/console'
import { cn } from '@/lib/cn'
import { formatTokens } from '@/lib/format'
import { Button } from '../Button'
import { Input } from '../Input'
import { Modal } from '../Modal'
import { Select } from '../Select'
import { Skeleton } from '../Skeleton'

export interface CreateKeyDialogProps {
  open: boolean
  onClose: () => void
  /** Receives the plaintext batch; the caller opens KeySecretOnce. */
  onCreated: (items: CreatedKey[], warning: string) => void
}

type Step = 1 | 2 | 3 | 4

const STEPS: { step: Step; label: string }[] = [
  { step: 1, label: '名称与数量' },
  { step: 2, label: '模型范围' },
  { step: 3, label: '有效期' },
  { step: 4, label: 'IP 白名单' },
]

function localDatetimeValue(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function parseIPList(text: string): string[] {
  return text
    .split(/[\n,;]+/)
    .map((item) => item.trim())
    .filter((item) => item.length > 0)
}

/**
 * F6-2 four-step create wizard: name → model scope → expiry → IP whitelist.
 *
 * There is deliberately no group step and no group_code field: new keys always
 * land in `standard`, and the backend rejects the field if a caller injects it
 * (F6-6 / F7-4).
 */
export function CreateKeyDialog({ open, onClose, onCreated }: CreateKeyDialogProps) {
  const mutation = useCreateKeys()
  const modelsQuery = useConsoleModels(open)
  const [step, setStep] = useState<Step>(1)
  const [name, setName] = useState('')
  const [count, setCount] = useState(String(KEY_BATCH_MIN))
  const [scopeMode, setScopeMode] = useState<ModelScopeMode>('ALL')
  const [selectedModels, setSelectedModels] = useState<number[]>([])
  const [expireAt, setExpireAt] = useState('')
  const [ipText, setIpText] = useState('')
  const [modelFilter, setModelFilter] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [hint, setHint] = useState<string | null>(null)

  // Recomputed on every render so a dialog left open across midnight (or a
  // laptop sleep) never offers a min in the past.
  const minExpire = localDatetimeValue(new Date(Date.now() + 60_000))

  useEffect(() => {
    if (!open) return
    setStep(1)
    setName('')
    setCount(String(KEY_BATCH_MIN))
    setScopeMode('ALL')
    setSelectedModels([])
    setExpireAt('')
    setIpText('')
    setModelFilter('')
    setError(null)
    setHint(null)
    mutation.reset()
    // mutation.reset is stable; open is the only trigger we want.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const models = modelsQuery.data ?? []
  const filteredModels = useMemo(() => {
    const keyword = modelFilter.trim().toLowerCase()
    if (!keyword) return models
    return models.filter(
      (model) =>
        model.model_name.toLowerCase().includes(keyword) ||
        model.display_name.toLowerCase().includes(keyword) ||
        model.aliases.some((alias) => alias.toLowerCase().includes(keyword)),
    )
  }, [modelFilter, models])

  if (!open) return null

  const validateStep = (current: Step): string | null => {
    if (current === 1) {
      const trimmed = name.trim()
      if (!trimmed) return '请输入密钥名称。'
      if (Array.from(trimmed).length > 64) return '密钥名称最多 64 个字符。'
      const parsed = Number(count)
      if (!Number.isInteger(parsed) || parsed < KEY_BATCH_MIN || parsed > KEY_BATCH_MAX) {
        return `批量数量需为 ${KEY_BATCH_MIN}–${KEY_BATCH_MAX} 的整数。`
      }
      return null
    }
    if (current === 2) {
      if (scopeMode === 'SELECTED' && selectedModels.length === 0) {
        return '请至少选择一个模型，或改为「全部模型」。'
      }
      return null
    }
    if (current === 3) {
      if (!expireAt) return '请设置到期时间。'
      if (new Date(expireAt).getTime() <= Date.now()) return '到期时间必须晚于当前时间。'
      return null
    }
    return null
  }

  const goNext = () => {
    const message = validateStep(step)
    if (message) {
      setError(message)
      return
    }
    setError(null)
    setHint(null)
    setStep((current) => Math.min(4, current + 1) as Step)
  }

  const goBack = () => {
    setError(null)
    setHint(null)
    setStep((current) => Math.max(1, current - 1) as Step)
  }

  const toggleModel = (id: number) => {
    setSelectedModels((current) =>
      current.includes(id) ? current.filter((item) => item !== id) : [...current, id],
    )
  }

  const changeScope = (mode: ModelScopeMode) => {
    setScopeMode(mode)
    if (mode === 'ALL') setSelectedModels([])
  }

  const submit = async () => {
    for (const candidate of [1, 2, 3] as Step[]) {
      const message = validateStep(candidate)
      if (message) {
        setStep(candidate)
        setError(message)
        return
      }
    }
    setError(null)
    setHint(null)

    try {
      const response = await mutation.mutateAsync({
        name: name.trim(),
        count: Number(count),
        model_scope_mode: scopeMode,
        ...(scopeMode === 'SELECTED' ? { model_ids: selectedModels } : {}),
        expire_at: new Date(expireAt).toISOString(),
        ip_allowlist: parseIPList(ipText),
      })
      onCreated(response.items, response.warning)
      onClose()
    } catch (err) {
      setError(errorMessage(err))
      setHint(errorHint(err))
      // The free-tier ceiling is a state conflict, not a typo: keep the dialog
      // open so the copy (which names the ceiling) stays visible.
      if (err instanceof ApiError && err.code === 'key_limit_reached') {
        setStep(1)
      }
    }
  }

  return (
    <Modal
      open={open}
      onClose={mutation.isPending ? () => undefined : onClose}
      title="新建密钥"
      description="新密钥默认使用标准分组；分组变更需联系运营。"
      size="lg"
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={mutation.isPending}>
            取消
          </Button>
          {step > 1 ? (
            <Button variant="secondary" onClick={goBack} disabled={mutation.isPending}>
              上一步
            </Button>
          ) : null}
          {step < 4 ? (
            <Button onClick={goNext}>下一步</Button>
          ) : (
            <Button onClick={() => void submit()} loading={mutation.isPending}>
              生成密钥
            </Button>
          )}
        </>
      }
    >
      <form
        id="create-key-form"
        onSubmit={(event) => {
          event.preventDefault()
          void submit()
        }}
        className="space-y-5"
      >
        <ol className="flex flex-wrap items-center gap-2 text-xs" aria-label="创建步骤">
          {STEPS.map((item) => {
            const active = item.step === step
            const done = item.step < step
            return (
              <li
                key={item.step}
                aria-current={active ? 'step' : undefined}
                className={cn(
                  'flex items-center gap-1.5 rounded-full border px-2.5 py-1',
                  active
                    ? 'border-primary-ring bg-primary-soft font-medium text-primary-strong'
                    : done
                      ? 'border-success bg-success-soft text-success'
                      : 'border-border text-ink-muted',
                )}
              >
                <span aria-hidden="true">{done ? '✓' : item.step}</span>
                {item.label}
              </li>
            )
          })}
        </ol>

        {step === 1 ? (
          <div className="space-y-4">
            <Input
              label="密钥名称"
              required
              value={name}
              maxLength={64}
              onChange={(event) => setName(event.target.value)}
              placeholder="例如：生产环境、测试环境"
              hint="最多 64 个字符，用于区分不同环境或用途。"
            />
            <Input
              label="创建数量"
              required
              type="number"
              min={KEY_BATCH_MIN}
              max={KEY_BATCH_MAX}
              step={1}
              value={count}
              onChange={(event) => setCount(event.target.value)}
              hint={`同一批次共享模型范围、有效期与 IP 白名单，可创建 ${KEY_BATCH_MIN}–${KEY_BATCH_MAX} 个。`}
            />
          </div>
        ) : null}

        {step === 2 ? (
          <div className="space-y-3">
            <Select
              label="模型范围"
              value={scopeMode}
              onChange={(event) => changeScope(event.target.value as ModelScopeMode)}
              options={[
                { value: 'ALL', label: '全部已上架模型' },
                { value: 'SELECTED', label: '指定模型' },
              ]}
            />

            {scopeMode === 'SELECTED' ? (
              <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                  <Input
                    aria-label="筛选模型"
                    placeholder="按模型名或别名筛选"
                    value={modelFilter}
                    onChange={(event) => setModelFilter(event.target.value)}
                  />
                </div>

                {modelsQuery.isLoading ? (
                  <div className="space-y-2" aria-busy="true">
                    <Skeleton className="h-8 w-full" />
                    <Skeleton className="h-8 w-full" />
                    <Skeleton className="h-8 w-3/4" />
                  </div>
                ) : modelsQuery.isError ? (
                  <div role="alert" className="rounded-lg border border-danger bg-danger-soft px-3 py-2 text-xs text-danger">
                    {errorMessage(modelsQuery.error)}
                    <Button
                      variant="ghost"
                      size="sm"
                      className="ml-2"
                      onClick={() => void modelsQuery.refetch()}
                    >
                      重试
                    </Button>
                  </div>
                ) : filteredModels.length === 0 ? (
                  <p className="px-1 text-xs text-ink-muted">没有匹配的模型。</p>
                ) : (
                  <ul className="max-h-64 space-y-1 overflow-y-auto rounded-lg border border-border p-2">
                    {filteredModels.map((model) => (
                      <li key={model.id}>
                        <label className="flex cursor-pointer items-start gap-2 rounded-md px-2 py-1.5 hover:bg-surface-muted">
                          <input
                            type="checkbox"
                            className="mt-1 h-4 w-4 accent-[var(--primary)]"
                            checked={selectedModels.includes(model.id)}
                            onChange={() => toggleModel(model.id)}
                          />
                          <span className="min-w-0">
                            <span className="block truncate text-sm text-ink">
                              {model.display_name}
                            </span>
                            <span className="block truncate text-xs text-ink-muted">
                              {model.model_name} · {formatTokens(model.context_length)}
                            </span>
                          </span>
                        </label>
                      </li>
                    ))}
                  </ul>
                )}
                <p className="text-xs text-ink-muted">已选择 {selectedModels.length} 个模型。</p>
              </div>
            ) : null}
          </div>
        ) : null}

        {step === 3 ? (
          <Input
            label="到期时间"
            required
            type="datetime-local"
            min={minExpire}
            value={expireAt}
            onChange={(event) => setExpireAt(event.target.value)}
            hint="用户创建的密钥必须设置有效期，到期后自动失效；永不过期需运营授权。"
          />
        ) : null}

        {step === 4 ? (
          <label className="block space-y-1.5">
            <span className="block text-sm font-medium text-ink">IP 白名单（可选）</span>
            <textarea
              value={ipText}
              onChange={(event) => setIpText(event.target.value)}
              rows={5}
              spellCheck={false}
              className="w-full rounded-lg border border-border-strong bg-surface px-3 py-2 font-mono text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              placeholder={'每行一个 IP 或 CIDR，例如：\n203.0.113.0/24\n198.51.100.7'}
              aria-label="IP 白名单"
            />
            <span className="block text-xs text-ink-muted">
              留空表示不限制来源 IP。支持 IPv4 / IPv6 地址与 CIDR。
            </span>
          </label>
        ) : null}

        {error ? (
          <div role="alert" className="rounded-lg border border-danger bg-danger-soft px-3 py-2 text-xs">
            <p className="text-danger">{error}</p>
            {hint ? <p className="mt-0.5 text-ink-secondary">{hint}</p> : null}
          </div>
        ) : null}
      </form>
    </Modal>
  )
}
