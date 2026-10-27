import { useEffect, useState } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import {
  useCreateOrder,
  type CreateOrderInput,
  type CreatedOrder,
  type Plan,
} from '@/api/console'
import { Badge, Button, ErrorState, Modal, Select } from '@/components'
import { payChannelLabel } from '@/lib/consoleLabels'
import { formatDateTime, formatPlanPrice, formatYuanFromFen } from '@/lib/format'

export interface PayTarget {
  orderType: 'PLAN' | 'CUSTOM'
  plan?: Plan
  payAmountFen?: number
}

export interface PayModalProps {
  open: boolean
  target: PayTarget | null
  onClose: () => void
}

type PayChannel = 'WECHAT' | 'ALIPAY'

/**
 * Creates a PENDING order and stops there (F10-2). Credits move only on the
 * channel callback, so the success panel says "待支付" and never announces an
 * arrival that has not happened.
 */
export function PayModal({ open, target, onClose }: PayModalProps) {
  const mutation = useCreateOrder()
  const [channel, setChannel] = useState<PayChannel>('WECHAT')
  const [created, setCreated] = useState<CreatedOrder | null>(null)
  const [failure, setFailure] = useState<{ message: string; hint: string | null } | null>(null)

  const targetKey = target
    ? `${target.orderType}:${target.plan?.id ?? ''}:${target.payAmountFen ?? ''}`
    : ''

  useEffect(() => {
    if (!open) return
    setCreated(null)
    setFailure(null)
    setChannel('WECHAT')
  }, [open, targetKey])

  if (!open || !target) return null

  const submit = async () => {
    setFailure(null)
    const input: CreateOrderInput =
      target.orderType === 'PLAN' && target.plan
        ? { order_type: 'PLAN', plan_id: target.plan.id, pay_channel: channel }
        : {
            order_type: 'CUSTOM',
            pay_amount_fen: target.payAmountFen ?? 0,
            pay_channel: channel,
          }
    try {
      const response = await mutation.mutateAsync(input)
      setCreated(response)
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
      title={created ? '订单已创建' : target.orderType === 'PLAN' ? '购买套餐' : '自定义充值'}
      description={created ? undefined : '金额与到账积分由服务端计算，下单不扣款。'}
      size="sm"
      footer={
        created ? (
          <Button onClick={close}>关闭</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={close} disabled={mutation.isPending}>
              取消
            </Button>
            <Button onClick={() => void submit()} loading={mutation.isPending}>
              创建订单
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={created.order.state === 'PENDING' ? 'warning' : 'success'} dot>
              {created.order.state === 'PENDING'
                ? '待支付'
                : created.order.state === 'PAID'
                  ? '已支付'
                  : created.order.state}
            </Badge>
            {created.idempotent_replay ? <Badge variant="neutral">重复提交复用</Badge> : null}
          </div>

          <dl className="space-y-2 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">订单号</dt>
              <dd className="break-all font-mono text-xs text-ink">{created.order.order_no}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">应付金额</dt>
              <dd className="font-medium tabular-nums text-ink">
                {formatYuanFromFen(created.order.pay_amount_fen)} 元
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">支付方式</dt>
              <dd className="text-ink">{payChannelLabel(created.order.pay_channel)}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">支付截止</dt>
              <dd className="text-xs text-ink-secondary">{formatDateTime(created.order.expire_at)}</dd>
            </div>
          </dl>

          <p className="rounded-lg border border-warning bg-warning-soft px-3 py-2 text-xs text-warning">
            当前状态为待支付。支付渠道回调确认后系统才会入账，请在本页刷新订单状态核实。
          </p>
        </div>
      ) : (
        <div className="space-y-4">
          <dl className="space-y-2 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">商品</dt>
              <dd className="text-ink">
                {target.orderType === 'PLAN' ? target.plan?.name : '自定义充值'}
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">应付金额</dt>
              <dd className="font-medium tabular-nums text-ink">
                {target.orderType === 'PLAN' && target.plan
                  ? formatPlanPrice(target.plan.price_fen)
                  : `${formatYuanFromFen(target.payAmountFen ?? 0)} 元`}
              </dd>
            </div>
            {target.orderType === 'PLAN' && target.plan ? (
              <div className="flex justify-between gap-4">
                <dt className="text-ink-secondary">套餐积分</dt>
                <dd className="text-ink">
                  充值 {target.plan.grant_recharge} 积分 · 赠送 {target.plan.grant_bonus} 积分
                </dd>
              </div>
            ) : null}
          </dl>

          <Select
            label="支付方式"
            value={channel}
            onChange={(event) => setChannel(event.target.value as PayChannel)}
            options={[
              { value: 'WECHAT', label: payChannelLabel('WECHAT') },
              { value: 'ALIPAY', label: payChannelLabel('ALIPAY') },
            ]}
          />

          {failure ? (
            <ErrorState compact message={failure.message} hint={failure.hint} />
          ) : null}
        </div>
      )}
    </Modal>
  )
}
