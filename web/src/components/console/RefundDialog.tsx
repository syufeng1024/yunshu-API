import { useEffect, useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import { useCreateRefund, type CreatedRefund, type Order } from '@/api/console'
import { Badge, Button, ErrorState, Modal } from '@/components'
import { refundStateLabel } from '@/lib/consoleLabels'
import { formatDateTime, formatInteger, formatYuanFromFen } from '@/lib/format'

export interface RefundDialogProps {
  open: boolean
  order: Order | null
  onClose: () => void
}

/**
 * Refund application. POST /api/console/refunds takes only the order id: the
 * quote (which part of the recharge is still unspent, what fee applies) is
 * computed and frozen server-side in one transaction. There is deliberately no
 * amount input, because the API has no amount field to send.
 */
export function RefundDialog({ open, order, onClose }: RefundDialogProps) {
  const mutation = useCreateRefund()
  const [created, setCreated] = useState<CreatedRefund | null>(null)
  const [failure, setFailure] = useState<{ message: string; hint: string | null } | null>(null)

  useEffect(() => {
    if (!open) return
    setCreated(null)
    setFailure(null)
  }, [open, order?.id])

  if (!open || !order) return null

  const submit = async () => {
    setFailure(null)
    try {
      setCreated(await mutation.mutateAsync(order.id))
    } catch (error) {
      setFailure({ message: errorMessage(error), hint: errorHint(error) })
    }
  }

  const close = () => {
    if (mutation.isPending) return
    setCreated(null)
    setFailure(null)
    onClose()
  }

  return (
    <Modal
      open={open}
      onClose={close}
      title={created ? '退款申请已受理' : '申请退款'}
      description={created ? undefined : '只退未消耗的充值积分；赠送积分不可退。'}
      size="sm"
      footer={
        created ? (
          <Button onClick={close}>关闭</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={close} disabled={mutation.isPending}>
              取消
            </Button>
            <Button
              variant="danger"
              onClick={() => void submit()}
              loading={mutation.isPending}
            >
              提交退款申请
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div className="space-y-3 text-sm">
          <Badge variant="warning" dot>
            {refundStateLabel(created.refund.state)}
          </Badge>
          <dl className="space-y-2">
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">退款单号</dt>
              <dd className="break-all font-mono text-xs text-ink">{created.refund.refund_no}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">申请积分</dt>
              <dd className="tabular-nums text-ink">
                {formatInteger(created.refund.apply_credits)} 积分
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">退回金额</dt>
              <dd className="tabular-nums text-ink">
                {formatYuanFromFen(created.refund.refund_fen)} 元
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">手续费</dt>
              <dd className="tabular-nums text-ink">
                {formatYuanFromFen(created.refund.fee_fen)} 元
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">申请时间</dt>
              <dd className="text-xs text-ink-secondary">
                {formatDateTime(created.refund.created_at)}
              </dd>
            </div>
          </dl>
          <p className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-xs text-ink-secondary">
            申请提交后对应余额即冻结，需等待运营审核与渠道退款；被驳回或失败时冻结自动解除。
          </p>
        </div>
      ) : (
        <div className="space-y-4">
          <dl className="space-y-2 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">订单号</dt>
              <dd className="break-all font-mono text-xs text-ink">{order.order_no}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">实付金额</dt>
              <dd className="tabular-nums text-ink">{formatYuanFromFen(order.pay_amount_fen)} 元</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">充值积分</dt>
              <dd className="tabular-nums text-ink">{formatInteger(order.recharge)} 积分</dd>
            </div>
          </dl>

          <p className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-xs text-ink-secondary">
            可退积分基数由服务端按该订单尚未消耗的充值积分实时计算，提交后按该基数冻结并原路退回；手续费同样由服务端返回。
          </p>

          {failure ? (
            <ErrorState compact message={failure.message} hint={failure.hint} />
          ) : null}
        </div>
      )}
    </Modal>
  )
}
