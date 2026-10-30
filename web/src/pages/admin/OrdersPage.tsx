import { useEffect, useMemo, useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import {
  useAdminOrders,
  useCloseAdminOrder,
  useConfirmOrderPayment,
  type AdminOrder,
  type AdminOrderStateFilter,
} from '@/api/admin'
import {
  Badge,
  Button,
  ErrorState,
  Input,
  Modal,
  PageHeader,
  Select,
  Table,
  useToast,
} from '@/components'
import type { BadgeVariant, TableColumn } from '@/components'
import { orderStateLabel, payChannelLabel } from '@/lib/consoleLabels'
import { formatDateTime, formatInteger, formatYuanFromFen } from '@/lib/format'
import { useDebouncedValue } from '@/lib/hooks/useDebouncedValue'
import { usePageTitle } from '@/lib/usePageTitle'

const PAGE_SIZE = 20

/** Mirrors the handler's bank_txn_no rule: 6–64 of [A-Za-z0-9_-]. */
const BANK_TXN_NO_PATTERN = /^[A-Za-z0-9_-]{6,64}$/
const NOTE_MAX = 255

/** Mirrors the close handler's reason rule (closeReasonMin/Max). */
const CLOSE_REASON_MIN = 2
const CLOSE_REASON_MAX = 100

const STATE_OPTIONS: { value: AdminOrderStateFilter; label: string }[] = [
  { value: 'pending', label: '待支付' },
  { value: 'paid', label: '已支付' },
  { value: 'closed', label: '已关闭' },
  { value: 'refunding', label: '退款中' },
  { value: 'refunded', label: '已退款 / 部分退款' },
  { value: 'ALL', label: '全部状态' },
]

const STATE_BADGE: Record<string, BadgeVariant> = {
  PENDING: 'warning',
  PAID: 'success',
  CLOSED: 'neutral',
  REFUNDING: 'warning',
  REFUNDED: 'info',
  PARTIAL_REFUNDED: 'info',
}

/**
 * The pending-payment queue finance works from.
 *
 * A customer who pays by 对公转账 never triggers a channel callback, so the
 * order stays PENDING until a human reads the bank statement and confirms the
 * arrival here. The confirmation is the same transaction the callback runs, so
 * the credited amount and the audit trail are identical; the bank statement
 * number is what makes a second confirmation a replay instead of a second
 * credit.
 */
export default function AdminOrdersPage() {
  usePageTitle('订单与收款')
  const toast = useToast()

  // The default tab is the one finance acts on: money that has not arrived.
  const [state, setState] = useState<AdminOrderStateFilter>('pending')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const debouncedSearch = useDebouncedValue(search, 300)

  useEffect(() => {
    setPage(1)
  }, [debouncedSearch, state])

  const orders = useAdminOrders({
    state,
    q: debouncedSearch || undefined,
    page,
    pageSize: PAGE_SIZE,
  })
  const confirm = useConfirmOrderPayment()
  const closeMutation = useCloseAdminOrder()

  const [target, setTarget] = useState<AdminOrder | null>(null)
  const [bankTxnNo, setBankTxnNo] = useState('')
  const [note, setNote] = useState('')
  const [fieldError, setFieldError] = useState<string | null>(null)
  const [submitError, setSubmitError] = useState<{ message: string; hint: string | null } | null>(
    null,
  )

  const openConfirm = (order: AdminOrder) => {
    setTarget(order)
    setBankTxnNo('')
    setNote('')
    setFieldError(null)
    setSubmitError(null)
  }

  const closeConfirm = () => {
    if (confirm.isPending) return
    setTarget(null)
  }

  const [closeTarget, setCloseTarget] = useState<AdminOrder | null>(null)
  const [closeReason, setCloseReason] = useState('')
  const [closeFieldError, setCloseFieldError] = useState<string | null>(null)
  const [closeError, setCloseError] = useState<{ message: string; hint: string | null } | null>(
    null,
  )

  const openClose = (order: AdminOrder) => {
    setCloseTarget(order)
    setCloseReason('')
    setCloseFieldError(null)
    setCloseError(null)
  }

  const cancelClose = () => {
    if (closeMutation.isPending) return
    setCloseTarget(null)
  }

  const submitClose = async () => {
    if (!closeTarget) return
    const reason = closeReason.trim()
    setCloseFieldError(null)
    setCloseError(null)
    const length = [...reason].length
    if (length < CLOSE_REASON_MIN) {
      setCloseFieldError(`请填写作废原因（至少 ${CLOSE_REASON_MIN} 个字）。`)
      return
    }
    if (length > CLOSE_REASON_MAX) {
      setCloseFieldError(`作废原因最多 ${CLOSE_REASON_MAX} 个字符。`)
      return
    }
    try {
      const result = await closeMutation.mutateAsync({ id: closeTarget.id, input: { reason } })
      toast.success(
        result.replayed ? '该订单已处于已关闭状态，本次未重复作废' : '订单已关闭',
        result.replayed ? undefined : `订单 ${result.order.order_no} 已作废，客户无法再支付。`,
      )
      setCloseTarget(null)
    } catch (error) {
      setCloseError({ message: errorMessage(error), hint: errorHint(error) })
    }
  }

  const submit = async () => {
    if (!target) return
    const txn = bankTxnNo.trim()
    const trimmedNote = note.trim()
    setFieldError(null)
    setSubmitError(null)
    if (!BANK_TXN_NO_PATTERN.test(txn)) {
      setFieldError('请输入 6–64 位银行流水号（字母、数字、下划线或连字符）。')
      return
    }
    if ([...trimmedNote].length > NOTE_MAX) {
      setFieldError(`备注最多 ${NOTE_MAX} 个字符。`)
      return
    }
    try {
      const result = await confirm.mutateAsync({
        id: target.id,
        input: { bank_txn_no: txn, note: trimmedNote || undefined },
      })
      toast.success(
        result.replayed ? '该流水号已确认过，本次未重复入账' : '已确认收款并完成入账',
        result.replayed ? undefined : `订单 ${result.order.order_no} 已置为已支付。`,
      )
      setTarget(null)
    } catch (error) {
      setSubmitError({ message: errorMessage(error), hint: errorHint(error) })
    }
  }

  const columns: TableColumn<AdminOrder>[] = useMemo(
    () => [
      {
        key: 'order_no',
        header: '订单号',
        render: (order) => (
          <code className="break-all text-xs text-ink-secondary">{order.order_no}</code>
        ),
      },
      {
        key: 'user',
        header: '用户',
        render: (order) => (
          <div className="space-y-0.5">
            <p className="text-xs text-ink">
              {order.user_nickname || `用户#${order.user_id}`}
            </p>
            <p className="break-all text-xs text-ink-muted">{order.user_email || '—'}</p>
          </div>
        ),
      },
      {
        key: 'type',
        header: '类型',
        render: (order) => (
          <span className="text-xs text-ink-secondary">
            {order.order_type === 'PLAN' ? order.plan_code ?? '套餐' : '自定义充值'}
          </span>
        ),
      },
      {
        key: 'amount',
        header: '金额',
        align: 'right',
        render: (order) => (
          <div className="space-y-0.5 text-right">
            <p className="tabular-nums text-ink">{formatYuanFromFen(order.pay_amount_fen)} 元</p>
            <p className="text-xs tabular-nums text-ink-muted">
              充值 {formatInteger(order.recharge)} / 赠送 {formatInteger(order.bonus)}
            </p>
          </div>
        ),
      },
      {
        key: 'channel',
        header: '渠道',
        render: (order) => (
          <div className="space-y-0.5">
            <span className="text-xs text-ink-secondary">{payChannelLabel(order.pay_channel)}</span>
            {order.pay_txn_id ? (
              <p className="break-all text-xs text-ink-muted">{order.pay_txn_id}</p>
            ) : null}
          </div>
        ),
      },
      {
        key: 'state',
        header: '状态',
        render: (order) => (
          <Badge variant={STATE_BADGE[order.state] ?? 'neutral'} dot>
            {orderStateLabel(order.state)}
          </Badge>
        ),
      },
      {
        key: 'created',
        header: '创建时间',
        render: (order) => (
          <span className="text-xs text-ink-secondary">{formatDateTime(order.created_at)}</span>
        ),
      },
      {
        key: 'actions',
        header: '操作',
        align: 'right',
        render: (order) =>
          order.state === 'PENDING' ? (
            <div className="flex flex-wrap items-center justify-end gap-1">
              <Button variant="secondary" size="sm" onClick={() => openConfirm(order)}>
                确认收款
              </Button>
              <Button variant="ghost" size="sm" onClick={() => openClose(order)}>
                关闭
              </Button>
            </div>
          ) : (
            <span className="text-xs text-ink-muted">—</span>
          ),
      },
    ],
    [],
  )

  const listError = orders.isError ? errorMessage(orders.error) : null

  return (
    <div className="space-y-6">
      <PageHeader
        title="订单与收款"
        description="客户对公转账后，财务在此核对银行流水并确认收款；确认即按订单金额入账，并写入审计。"
      />

      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_12rem]">
        <Input
          label="按订单号或用户邮箱搜索"
          type="search"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder="输入订单号片段或完整邮箱"
          maxLength={64}
          aria-label="按订单号或用户邮箱搜索"
        />
        <Select
          label="订单状态"
          value={state}
          onChange={(event) => setState(event.target.value as AdminOrderStateFilter)}
          options={STATE_OPTIONS}
        />
      </div>

      <Table
        ariaLabel="运营订单列表"
        columns={columns}
        rows={orders.data?.items ?? []}
        rowKey={(order) => String(order.id)}
        loading={orders.isLoading}
        error={listError}
        onRetry={() => void orders.refetch()}
        emptyTitle="没有匹配的订单"
        emptyDescription={
          state === 'pending'
            ? '当前没有待支付的订单；切换到「全部状态」可以查看历史。'
            : '当前筛选条件下没有订单。'
        }
        pagination={
          orders.data
            ? {
                page: orders.data.page,
                pageSize: orders.data.page_size,
                total: orders.data.total,
                onPageChange: setPage,
              }
            : undefined
        }
      />

      <Modal
        open={target !== null}
        onClose={closeConfirm}
        title="确认收款"
        description={
          target
            ? `订单 ${target.order_no} · 应付 ${formatYuanFromFen(target.pay_amount_fen)} 元`
            : undefined
        }
        size="md"
        footer={
          <>
            <Button variant="secondary" onClick={closeConfirm} disabled={confirm.isPending}>
              取消
            </Button>
            <Button onClick={() => void submit()} loading={confirm.isPending}>
              确认到账
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <dl className="space-y-2 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">用户</dt>
              <dd className="break-all text-right text-ink">
                {target ? target.user_nickname || `用户#${target.user_id}` : ''}
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">入账充值 / 赠送积分</dt>
              <dd className="tabular-nums text-ink">
                {target ? `${formatInteger(target.recharge)} / ${formatInteger(target.bonus)}` : ''}
              </dd>
            </div>
          </dl>

          <Input
            label="银行流水号"
            value={bankTxnNo}
            onChange={(event) => {
              setBankTxnNo(event.target.value)
              setFieldError(null)
            }}
            placeholder="例如 20261030001"
            hint="必填，6–64 位字母、数字、下划线或连字符；同一流水号重复提交不会重复入账。"
            error={fieldError ?? undefined}
            autoComplete="off"
          />
          <Input
            label="备注（选填）"
            value={note}
            onChange={(event) => {
              setNote(event.target.value)
              setFieldError(null)
            }}
            placeholder="例如 对公转账 / 客户名称"
            maxLength={NOTE_MAX}
          />

          {target && new Date(target.expire_at).getTime() < Date.now() ? (
            // 过期只是提示，不阻止确认：钱真到了就该入账，超时到账由财务判断。
            <p className="rounded-lg border border-danger bg-danger-soft px-3 py-2 text-xs text-danger">
              该订单已过期（支付截止 {formatDateTime(target.expire_at)}），请核实款项是否到账后再确认。
            </p>
          ) : null}

          <p className="rounded-lg border border-warning bg-warning-soft px-3 py-2 text-xs text-warning">
            确认后订单立即转为已支付并给用户入账，该动作会写入审计，不能撤销。
          </p>

          {submitError ? (
            <ErrorState compact message={submitError.message} hint={submitError.hint} />
          ) : null}
        </div>
      </Modal>

      <Modal
        open={closeTarget !== null}
        onClose={cancelClose}
        title="关闭订单"
        description={
          closeTarget
            ? `订单 ${closeTarget.order_no} · 应付 ${formatYuanFromFen(closeTarget.pay_amount_fen)} 元`
            : undefined
        }
        size="md"
        footer={
          <>
            <Button variant="secondary" onClick={cancelClose} disabled={closeMutation.isPending}>
              取消
            </Button>
            <Button onClick={() => void submitClose()} loading={closeMutation.isPending}>
              确认关闭
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <dl className="space-y-2 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">用户</dt>
              <dd className="break-all text-right text-ink">
                {closeTarget ? closeTarget.user_nickname || `用户#${closeTarget.user_id}` : ''}
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">支付截止</dt>
              <dd className="text-xs text-ink-secondary">
                {closeTarget ? formatDateTime(closeTarget.expire_at) : ''}
              </dd>
            </div>
          </dl>

          <label className="block space-y-1.5">
            <span className="block text-sm font-medium text-ink">
              作废原因
              <span className="ml-0.5 text-danger" aria-hidden="true">
                *
              </span>
            </span>
            <textarea
              value={closeReason}
              onChange={(event) => {
                setCloseReason(event.target.value)
                setCloseFieldError(null)
              }}
              rows={3}
              maxLength={CLOSE_REASON_MAX}
              placeholder="例如 客户未转账 / 客户改走其他渠道"
              aria-label="作废原因"
              className="w-full rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            />
            {closeFieldError ? (
              <p className="text-xs text-danger">{closeFieldError}</p>
            ) : (
              <p className="text-xs text-ink-muted">
                2–100 个字符，会写入审计；关闭后客户无法再支付该订单，且不可撤销。
              </p>
            )}
          </label>

          {closeError ? (
            <ErrorState compact message={closeError.message} hint={closeError.hint} />
          ) : null}
        </div>
      </Modal>
    </div>
  )
}
