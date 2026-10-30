import { useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import {
  useCreateRedeemBatch,
  useRedeemBatches,
  type CreateRedeemBatchResult,
  type RedeemBatch,
  type RedeemCreditTarget,
} from '@/api/admin'
import {
  Badge,
  Button,
  ErrorState,
  Input,
  PageHeader,
  Select,
  Table,
  useToast,
} from '@/components'
import type { TableColumn } from '@/components'
import { writeClipboard } from '@/lib/clipboard'
import { formatDateTime, formatInteger, formatYuanFromFen, fenFromYuanInput } from '@/lib/format'
import { usePageTitle } from '@/lib/usePageTitle'

const PAGE_SIZE = 20

/** Server bounds (internal/control/fund/redeem_batch.go). */
const QUANTITY_MIN = 1
const QUANTITY_MAX = 500
const VALID_DAYS_MIN = 1
const VALID_DAYS_MAX = 3650
const FACE_VALUE_MAX_FEN = 65535
const BATCH_NAME_MAX = 64

const TARGET_OPTIONS: { value: RedeemCreditTarget; label: string }[] = [
  { value: 'BONUS', label: '赠送积分（不可退款）' },
  { value: 'RECHARGE', label: '充值积分（计入退款额度）' },
]

/**
 * BONUS is the default on purpose: B-44 A 档 fixed redeemed credit as bonus
 * credit, which cannot be refunded or cashed out. RECHARGE stays reachable
 * (the handler accepts both) but must be a deliberate choice, not a default —
 * it moves the amount into the refundable balance.
 */
const DEFAULT_CREDIT_TARGET: RedeemCreditTarget = 'BONUS'

function creditTargetLabel(target: string): string {
  return target === 'RECHARGE' ? '充值积分' : '赠送积分'
}

/**
 * Redeem-code batches (B-44 A 档).
 *
 * The plaintext exists once: in the response to this page. Leaving the page
 * loses it — the database keeps only SHA256(code) and a six-character prefix —
 * so the result panel is a one-time surface, not a table row.
 */
export default function AdminRedeemCodesPage() {
  usePageTitle('兑换码')
  const toast = useToast()

  const [batchName, setBatchName] = useState('')
  const [faceValueYuan, setFaceValueYuan] = useState('')
  const [quantity, setQuantity] = useState('10')
  const [creditTarget, setCreditTarget] = useState<RedeemCreditTarget>(DEFAULT_CREDIT_TARGET)
  const [validDays, setValidDays] = useState('90')
  const [formError, setFormError] = useState<string | null>(null)
  const [formField, setFormField] = useState<string | null>(null)
  const [submitError, setSubmitError] = useState<{ message: string; hint: string | null } | null>(
    null,
  )
  const [created, setCreated] = useState<CreateRedeemBatchResult | null>(null)

  const [page, setPage] = useState(1)
  const batches = useRedeemBatches({ page, pageSize: PAGE_SIZE })
  const generate = useCreateRedeemBatch()

  const resetForm = () => {
    setBatchName('')
    setFaceValueYuan('')
    setQuantity('10')
    setCreditTarget(DEFAULT_CREDIT_TARGET)
    setValidDays('90')
  }

  const fail = (field: string, message: string) => {
    setFormField(field)
    setFormError(message)
  }

  const submit = async () => {
    setFormError(null)
    setFormField(null)
    setSubmitError(null)

    const name = batchName.trim()
    if (!name) return fail('batch_name', '请输入批次名称。')
    if ([...name].length > BATCH_NAME_MAX) {
      return fail('batch_name', `批次名称最多 ${BATCH_NAME_MAX} 个字符。`)
    }

    const fen = fenFromYuanInput(faceValueYuan)
    if (fen === null || fen < 1) {
      return fail('face_value_fen', '请输入有效的面额（元，最多两位小数）。')
    }
    if (fen > FACE_VALUE_MAX_FEN) {
      return fail('face_value_fen', `单张面额不能超过 ${formatYuanFromFen(FACE_VALUE_MAX_FEN)} 元。`)
    }

    const count = Number(quantity)
    if (!Number.isInteger(count) || count < QUANTITY_MIN || count > QUANTITY_MAX) {
      return fail('quantity', `数量需为 ${QUANTITY_MIN}–${QUANTITY_MAX} 的整数。`)
    }

    const days = Number(validDays)
    if (!Number.isInteger(days) || days < VALID_DAYS_MIN || days > VALID_DAYS_MAX) {
      return fail('valid_days', `有效天数需为 ${VALID_DAYS_MIN}–${VALID_DAYS_MAX} 的整数。`)
    }

    try {
      const result = await generate.mutateAsync({
        batch_name: name,
        face_value_fen: fen,
        quantity: count,
        credit_target: creditTarget,
        valid_days: days,
      })
      setCreated(result)
      resetForm()
      setPage(1)
      toast.success(`已生成 ${result.codes.length} 张兑换码`, '明文只在下方显示一次，请立即导出。')
    } catch (error) {
      setSubmitError({ message: errorMessage(error), hint: errorHint(error) })
      return
    }
  }

  const copyAll = async () => {
    if (!created) return
    try {
      await writeClipboard(created.codes.join('\n'))
      toast.success('已复制全部兑换码')
    } catch {
      toast.error('复制失败', '浏览器未授予剪贴板权限，请手动选择文本复制。')
    }
  }

  const columns: TableColumn<RedeemBatch>[] = [
    {
      key: 'name',
      header: '批次名称',
      render: (batch) => (
        <div className="space-y-0.5">
          <p className="text-xs text-ink">{batch.batch_name}</p>
          <p className="text-xs text-ink-muted">#{batch.id}</p>
        </div>
      ),
    },
    {
      key: 'face',
      header: '面额',
      align: 'right',
      render: (batch) => (
        <div className="space-y-0.5 text-right">
          <p className="tabular-nums text-ink">{formatYuanFromFen(batch.face_value_fen)} 元</p>
          <p className="text-xs text-ink-muted">{creditTargetLabel(batch.credit_target)}</p>
        </div>
      ),
    },
    {
      key: 'counts',
      header: '总量 / 已用',
      align: 'right',
      render: (batch) => (
        <span className="text-xs tabular-nums text-ink-secondary">
          {formatInteger(batch.quantity)} / {formatInteger(batch.redeemed_count)}
        </span>
      ),
    },
    {
      key: 'valid',
      header: '有效期',
      render: (batch) => (
        <span className="text-xs text-ink-secondary">
          {formatDateTime(batch.valid_from)} — {formatDateTime(batch.valid_to)}
        </span>
      ),
    },
    {
      key: 'created',
      header: '创建时间',
      render: (batch) => (
        <div className="space-y-0.5">
          <p className="text-xs text-ink-secondary">{formatDateTime(batch.created_at)}</p>
          {batch.created_by_email ? (
            <p className="break-all text-xs text-ink-muted">{batch.created_by_email}</p>
          ) : null}
        </div>
      ),
    },
  ]

  const listError = batches.isError ? errorMessage(batches.error) : null

  return (
    <div className="space-y-6">
      <PageHeader
        title="兑换码"
        description="批量生成面额固定的兑换码交给客户；库内只存哈希与掩码，明文仅生成时返回一次。"
      />

      <section className="space-y-3 rounded-xl border border-border bg-surface p-5 shadow-card">
        <div className="space-y-1">
          <h2 className="text-sm font-semibold text-ink">生成批次</h2>
          <p className="text-xs text-ink-secondary">
            单次 1–500 张，面额上限 {formatYuanFromFen(FACE_VALUE_MAX_FEN)} 元，有效期 1–3650 天。
          </p>
        </div>

        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          <Input
            label="批次名称"
            value={batchName}
            onChange={(event) => setBatchName(event.target.value)}
            placeholder="例如 2026 国庆活动"
            maxLength={BATCH_NAME_MAX}
            error={formField === 'batch_name' ? formError ?? undefined : undefined}
          />
          <Input
            label="单张面额（元）"
            inputMode="decimal"
            value={faceValueYuan}
            onChange={(event) => setFaceValueYuan(event.target.value)}
            placeholder="例如 100"
            error={formField === 'face_value_fen' ? formError ?? undefined : undefined}
          />
          <Input
            label="生成数量"
            inputMode="numeric"
            value={quantity}
            onChange={(event) => setQuantity(event.target.value)}
            error={formField === 'quantity' ? formError ?? undefined : undefined}
          />
          <Select
            label="计入"
            value={creditTarget}
            onChange={(event) => setCreditTarget(event.target.value as RedeemCreditTarget)}
            options={TARGET_OPTIONS}
          />
          <Input
            label="有效天数"
            inputMode="numeric"
            value={validDays}
            onChange={(event) => setValidDays(event.target.value)}
            error={formField === 'valid_days' ? formError ?? undefined : undefined}
          />
          <div className="flex items-end">
            <Button onClick={() => void submit()} loading={generate.isPending}>
              生成兑换码
            </Button>
          </div>
        </div>

        {formError && !formField ? (
          <p role="alert" className="text-xs text-danger">
            {formError}
          </p>
        ) : null}
        {submitError ? (
          <ErrorState compact message={submitError.message} hint={submitError.hint} />
        ) : null}
      </section>

      {created ? (
        <section
          role="status"
          className="space-y-3 rounded-xl border border-success bg-success-soft p-5 text-success"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h2 className="text-sm font-semibold">
                批次 #{created.batch.id} · {created.batch.batch_name}
              </h2>
              <p className="text-xs">
                共 {formatInteger(created.codes.length)} 张 · 面额{' '}
                {formatYuanFromFen(created.batch.face_value_fen)} 元 ·{' '}
                {creditTargetLabel(created.batch.credit_target)} · 有效期至{' '}
                {formatDateTime(created.batch.valid_to)}
              </p>
            </div>
            <div className="flex items-center gap-2">
              <Badge variant="success">仅显示一次</Badge>
              <Button variant="secondary" size="sm" onClick={() => void copyAll()}>
                复制全部
              </Button>
            </div>
          </div>

          <p className="rounded-lg border border-warning bg-warning-soft px-3 py-2 text-xs text-warning">
            离开本页后不再显示明文：系统只保存兑换码的哈希与掩码，无法再次导出。请立即复制或下载保存。
          </p>

          <pre
            data-testid="redeem-code-plaintext"
            className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-border bg-surface px-3 py-2 font-mono text-xs text-ink"
          >
            {created.codes.join('\n')}
          </pre>

          <Button variant="ghost" size="sm" onClick={() => setCreated(null)}>
            我已保存，关闭明文
          </Button>
        </section>
      ) : null}

      <section className="space-y-3">
        <div className="space-y-1">
          <h2 className="text-sm font-semibold text-ink">批次列表</h2>
          <p className="text-xs text-ink-secondary">已用数量随兑换实时累加，可据此判断活动进度。</p>
        </div>
        <Table
          ariaLabel="兑换码批次列表"
          columns={columns}
          rows={batches.data?.items ?? []}
          rowKey={(batch) => String(batch.id)}
          loading={batches.isLoading}
          error={listError}
          onRetry={() => void batches.refetch()}
          emptyTitle="暂无批次"
          emptyDescription="生成第一批兑换码后会在这里显示面额、数量与使用进度。"
          pagination={
            batches.data
              ? {
                  page: batches.data.page,
                  pageSize: batches.data.page_size,
                  total: batches.data.total,
                  onPageChange: setPage,
                }
              : undefined
          }
        />
      </section>
    </div>
  )
}
