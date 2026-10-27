import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { ApiError, errorHint, errorMessage } from '@/api/client'
import {
  useCreateInvoice,
  type CreateInvoiceInput,
  type CreatedInvoice,
  type Order,
} from '@/api/console'
import { Badge, Button, ErrorState, Input, Modal, Select } from '@/components'
import { invoiceStateLabel } from '@/lib/consoleLabels'
import { ROUTES } from '@/lib/constants'
import { fenFromYuanInput, formatYuanFromFen } from '@/lib/format'

export interface InvoiceDialogProps {
  open: boolean
  order: Order | null
  onClose: () => void
}

type TitleType = 'PERSONAL' | 'COMPANY'

/**
 * Invoice application. A 403 account_not_verified is a business gate, not a
 * failure: it renders as guidance with a link to support instead of a red
 * error state (the V1.0 real-name flow is handled by operations).
 */
export function InvoiceDialog({ open, order, onClose }: InvoiceDialogProps) {
  const mutation = useCreateInvoice()
  const [titleType, setTitleType] = useState<TitleType>('PERSONAL')
  const [title, setTitle] = useState('')
  const [taxNo, setTaxNo] = useState('')
  const [email, setEmail] = useState('')
  const [amountInput, setAmountInput] = useState('')
  const [amountError, setAmountError] = useState<string | null>(null)
  const [failure, setFailure] = useState<{
    code: string
    message: string
    hint: string | null
  } | null>(null)
  const [created, setCreated] = useState<CreatedInvoice | null>(null)

  useEffect(() => {
    if (!open) return
    setTitleType('PERSONAL')
    setTitle('')
    setTaxNo('')
    setEmail('')
    setAmountInput('')
    setAmountError(null)
    setFailure(null)
    setCreated(null)
  }, [open, order?.id])

  if (!open || !order) return null

  const submit = async () => {
    setAmountError(null)
    setFailure(null)

    let amountFen: number | undefined
    if (amountInput.trim() !== '') {
      const parsed = fenFromYuanInput(amountInput)
      if (parsed === null) {
        setAmountError('请输入有效的开票金额（最多两位小数）')
        return
      }
      amountFen = parsed
    }

    if (titleType === 'COMPANY' && taxNo.trim() === '') {
      setFailure({ code: 'invalid_request_body', message: '企业抬头需填写税号', hint: null })
      return
    }

    const input: CreateInvoiceInput = {
      recharge_order_id: order.id,
      title_type: titleType,
      title: title.trim(),
      receive_email: email.trim(),
    }
    if (titleType === 'COMPANY') input.tax_no = taxNo.trim()
    if (amountFen !== undefined) input.amount_fen = amountFen

    try {
      setCreated(await mutation.mutateAsync(input))
    } catch (error) {
      setFailure({
        code: error instanceof ApiError ? error.code : 'unknown',
        message: errorMessage(error),
        hint: errorHint(error),
      })
    }
  }

  const close = () => {
    if (mutation.isPending) return
    onClose()
  }

  const notVerified = failure?.code === 'account_not_verified'

  return (
    <Modal
      open={open}
      onClose={close}
      title={created ? '开票申请已受理' : '申请开票'}
      description={created ? undefined : '金额上限为实付金额减去已开票金额，由服务端校验。'}
      size="md"
      footer={
        created ? (
          <Button onClick={close}>关闭</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={close} disabled={mutation.isPending}>
              取消
            </Button>
            <Button onClick={() => void submit()} loading={mutation.isPending}>
              提交开票申请
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div className="space-y-3 text-sm">
          <Badge variant="warning" dot>
            {invoiceStateLabel(created.invoice.state)}
          </Badge>
          <dl className="space-y-2">
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">申请单号</dt>
              <dd className="break-all font-mono text-xs text-ink">{created.invoice.apply_no}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">开票金额</dt>
              <dd className="tabular-nums text-ink">
                {formatYuanFromFen(created.invoice.amount_fen)} 元
              </dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">接收邮箱</dt>
              <dd className="break-all text-xs text-ink-secondary">
                {created.invoice.receive_email}
              </dd>
            </div>
          </dl>
          <p className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-xs text-ink-secondary">
            运营核验实名与实付后开具，进度可在下方开票记录中查看。
          </p>
        </div>
      ) : (
        <div className="space-y-4">
          {notVerified ? (
            <div
              role="status"
              className="space-y-2 rounded-lg border border-warning bg-warning-soft px-4 py-3 text-xs text-warning"
            >
              <p className="text-sm font-medium">该账号未完成实名认证</p>
              <p>{failure.message}</p>
              {failure.hint ? <p>{failure.hint}</p> : null}
              <p>
                完成实名认证后回到本页重新提交；如无法自助认证，请
                <Link to={ROUTES.consoleTickets} className="mx-1 underline">
                  提交工单
                </Link>
                由运营核验。
              </p>
            </div>
          ) : null}

          <dl className="space-y-2 text-sm">
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">订单号</dt>
              <dd className="break-all font-mono text-xs text-ink">{order.order_no}</dd>
            </div>
            <div className="flex justify-between gap-4">
              <dt className="text-ink-secondary">实付金额</dt>
              <dd className="tabular-nums text-ink">{formatYuanFromFen(order.pay_amount_fen)} 元</dd>
            </div>
          </dl>

          <Select
            label="抬头类型"
            value={titleType}
            onChange={(event) => setTitleType(event.target.value as TitleType)}
            options={[
              { value: 'PERSONAL', label: '个人' },
              { value: 'COMPANY', label: '企业' },
            ]}
          />

          <Input
            label="发票抬头"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            maxLength={128}
            required
          />

          {titleType === 'COMPANY' ? (
            <Input
              label="税号"
              value={taxNo}
              onChange={(event) => setTaxNo(event.target.value)}
              maxLength={32}
              required
            />
          ) : null}

          <Input
            label="接收邮箱"
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            maxLength={254}
            required
          />

          <Input
            label="开票金额（元，可留空）"
            inputMode="decimal"
            value={amountInput}
            onChange={(event) => setAmountInput(event.target.value)}
            hint="留空表示按该订单剩余可开票金额申请；超过上限时服务端拒绝。"
            error={amountError ?? undefined}
          />

          {failure && !notVerified ? (
            <ErrorState compact message={failure.message} hint={failure.hint} />
          ) : null}
        </div>
      )}
    </Modal>
  )
}
