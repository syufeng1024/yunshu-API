import { useEffect, useMemo, useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import {
  useAdminOrders,
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
            <Button variant="secondary" size="sm" onClick={() => openConfirm(order)}>
              确认收款
            </Button>
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

          <p className="rounded-lg border border-warning bg-warning-soft px-3 py-2 text-xs text-warning">
            确认后订单立即转为已支付并给用户入账，该动作会写入审计，不能撤销。
          </p>

          {submitError ? (
            <ErrorState compact message={submitError.message} hint={submitError.hint} />
          ) : null}
        </div>
      </Modal>
    </div>
  )
}
