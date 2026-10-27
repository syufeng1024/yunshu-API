import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import { useCreateTicket } from '@/api/console'
import type {
  CreateTicketInput,
  CreatedTicket,
  RequestIDItem,
  TicketType,
} from '@/api/console'
import { Button, ErrorState, Input, Modal, Select } from '@/components'
import { ticketStateLabel, ticketTypeLabel } from '@/lib/consoleLabels'
import { RequestIDPicker } from './RequestIDPicker'

const TITLE_MAX = 120
const DESCRIPTION_MAX = 5000
const REQUEST_ID_LENGTH = 36

const TYPE_OPTIONS = (['BILLING', 'TECH', 'CONTENT_APPEAL', 'OTHER'] as const).map(
  (value) => ({ value, label: ticketTypeLabel(value) }),
)

function codePointLength(value: string): number {
  return Array.from(value).length
}

export interface TicketCreateDialogProps {
  open: boolean
  onClose: () => void
}

/**
 * Ticket creation. Content appeals can only link a request by pasting the
 * request_id from the gateway's 403 body: blocked requests are not written to
 * t_usage_record, so they never appear in `/request-ids`, and there is no
 * pre-submit hit-lookup endpoint. The rule ID is never an input — it is read
 * from the 201 response (F19-3).
 */
export function TicketCreateDialog({ open, onClose }: TicketCreateDialogProps) {
  const [ticketType, setTicketType] = useState<TicketType | ''>('')
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [selectedRequest, setSelectedRequest] = useState<RequestIDItem | null>(null)
  const [manualRequestId, setManualRequestId] = useState('')
  const [created, setCreated] = useState<CreatedTicket | null>(null)
  const [fieldError, setFieldError] = useState<string | null>(null)

  const mutation = useCreateTicket()

  useEffect(() => {
    if (!open) {
      setTicketType('')
      setTitle('')
      setDescription('')
      setSelectedRequest(null)
      setManualRequestId('')
      setCreated(null)
      setFieldError(null)
      mutation.reset()
    }
    // mutation identity is stable for the lifetime of the dialog.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const isAppeal = ticketType === 'CONTENT_APPEAL'
  const descriptionLength = codePointLength(description)
  const mutationError = mutation.isError ? errorMessage(mutation.error) : null
  const mutationHint = mutation.isError ? errorHint(mutation.error) : null

  const handleTypeChange = (value: string) => {
    setTicketType(value as TicketType | '')
    setFieldError(null)
    if (value !== 'CONTENT_APPEAL') setManualRequestId('')
    if (value === 'CONTENT_APPEAL') setSelectedRequest(null)
  }

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!ticketType) {
      setFieldError('请选择工单类型。')
      return
    }
    if (title.trim().length === 0) {
      setFieldError('请填写标题。')
      return
    }
    if (codePointLength(title.trim()) > TITLE_MAX) {
      setFieldError(`标题不能超过 ${TITLE_MAX} 个字符。`)
      return
    }
    if (description.trim().length === 0) {
      setFieldError('请填写正文。')
      return
    }
    if (descriptionLength > DESCRIPTION_MAX) {
      setFieldError(`正文不能超过 ${DESCRIPTION_MAX} 个字符。`)
      return
    }

    const requestID = isAppeal
      ? manualRequestId.trim()
      : (selectedRequest?.request_id ?? '')
    if (isAppeal && requestID.length !== REQUEST_ID_LENGTH) {
      setFieldError(
        `内容申诉需要填写 ${REQUEST_ID_LENGTH} 位 request_id，请从网关 403 错误的 request_id 字段复制。`,
      )
      return
    }

    setFieldError(null)
    const input: CreateTicketInput = {
      ticket_type: ticketType,
      title: title.trim(),
      description: description.trim(),
    }
    if (requestID) input.request_id = requestID
    mutation.mutate(input, { onSuccess: (data) => setCreated(data) })
  }

  return (
    <Modal
      open={open}
      onClose={mutation.isPending ? () => undefined : onClose}
      title={created ? '工单已提交' : '新建工单'}
      description={
        created
          ? '工单已创建，可在列表中跟踪首次响应与答复进度。'
          : '纯文本描述，不支持富文本与附件。'
      }
      size="lg"
      footer={
        created ? (
          <Button onClick={onClose}>关闭</Button>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose} disabled={mutation.isPending}>
              取消
            </Button>
            <Button
              type="submit"
              form="ticket-create-form"
              loading={mutation.isPending}
            >
              提交工单
            </Button>
          </>
        )
      }
    >
      {created ? (
        <div role="status" className="space-y-3 rounded-lg border border-success bg-success-soft px-4 py-3">
          <p className="text-sm font-medium text-ink">
            工单 {created.ticket.ticket_no} 已提交，当前状态：
            {ticketStateLabel(created.ticket.state)}
          </p>
          {created.ticket.request_id ? (
            <p className="break-all text-xs text-ink-secondary">
              关联请求：<span className="font-mono">{created.ticket.request_id}</span>
            </p>
          ) : null}

          {created.ticket.ticket_type === 'CONTENT_APPEAL' ? (
            <dl
              aria-label="内容申诉自动带出信息"
              className="grid gap-2 rounded-md bg-surface px-3 py-2 text-xs sm:grid-cols-3"
            >
              <div>
                <dt className="text-ink-muted">命中规则</dt>
                <dd className="break-words text-ink">
                  {created.ticket.policy_rule_id ?? '—'}
                </dd>
              </div>
              <div>
                <dt className="text-ink-muted">规则分类</dt>
                <dd className="break-words text-ink">
                  {created.ticket.policy_category ?? '—'}
                </dd>
              </div>
              <div>
                <dt className="text-ink-muted">命中请求</dt>
                <dd className="break-all font-mono text-ink">
                  {created.ticket.request_id ?? '—'}
                </dd>
              </div>
            </dl>
          ) : null}

          {created.notice ? (
            <p className="text-xs text-ink-secondary">{created.notice}</p>
          ) : null}

          <p className="text-xs text-ink-muted">
            规则 ID 由后端按命中记录带出，页面不提供手工填写入口。
          </p>
        </div>
      ) : (
        <form id="ticket-create-form" onSubmit={handleSubmit} className="space-y-4">
          <Select
            label="类型"
            required
            value={ticketType}
            onChange={(event) => handleTypeChange(event.target.value)}
            options={TYPE_OPTIONS}
            placeholder="请选择工单类型"
          />

          <Input
            label="标题"
            required
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            maxLength={TITLE_MAX}
            placeholder="一句话说明问题"
            hint={`${codePointLength(title)}/${TITLE_MAX}`}
          />

          <label className="block space-y-1.5">
            <span className="block text-sm font-medium text-ink">
              正文
              <span className="ml-0.5 text-danger" aria-hidden="true">
                *
              </span>
            </span>
            <textarea
              value={description}
              onChange={(event) => setDescription(event.target.value)}
              rows={6}
              className="w-full rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm text-ink placeholder:text-ink-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              placeholder="描述现象、复现步骤与期望结果"
              aria-label="正文"
            />
            <span
              className={
                descriptionLength > DESCRIPTION_MAX
                  ? 'block text-xs text-danger'
                  : 'block text-xs text-ink-muted'
              }
            >
              {descriptionLength}/{DESCRIPTION_MAX}
            </span>
          </label>

          {isAppeal ? (
            <div className="space-y-2">
              <Input
                label="关联请求 ID"
                value={manualRequestId}
                onChange={(event) => setManualRequestId(event.target.value)}
                maxLength={REQUEST_ID_LENGTH}
                placeholder="粘贴 36 位 request_id"
                hint="从网关 403 错误的 request_id 字段复制。被内容策略拦截的请求不会出现在请求选择器中。"
              />
              <p className="text-xs text-ink-muted">
                命中规则由后端按该请求的命中记录带出，提交后只读展示。
              </p>
            </div>
          ) : ticketType ? (
            <div className="space-y-2">
              <p className="text-sm font-medium text-ink">关联请求（可选）</p>
              <RequestIDPicker
                selected={selectedRequest}
                onSelect={setSelectedRequest}
                onClear={() => setSelectedRequest(null)}
                enabled={open}
              />
            </div>
          ) : null}

          {fieldError ? (
            <div role="alert" className="rounded-lg border border-danger bg-danger-soft px-3 py-2 text-xs">
              <p className="text-danger">{fieldError}</p>
            </div>
          ) : null}

          {mutationError ? (
            <ErrorState compact message={mutationError} hint={mutationHint} />
          ) : null}
        </form>
      )}
    </Modal>
  )
}
