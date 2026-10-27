import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { errorHint, errorMessage } from '@/api/client'
import { useCloseTicket, useTicket } from '@/api/console'
import type { Ticket, TicketState } from '@/api/console'
import type { BadgeVariant } from '@/components'
import { Badge, Button, Drawer, ErrorState, Skeleton } from '@/components'
import {
  replyAuthorLabel,
  ticketStateLabel,
  ticketTypeLabel,
} from '@/lib/consoleLabels'
import { formatDateTimeMs, formatInteger } from '@/lib/format'

function stateVariant(state: TicketState): BadgeVariant {
  if (state === 'RESPONDED') return 'primary'
  if (state === 'OPEN') return 'warning'
  return 'neutral'
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-ink-muted">{label}</dt>
      <dd className="break-words text-ink">{children}</dd>
    </div>
  )
}

export interface TicketDetailDrawerProps {
  ticketId: number | null
  onClose: () => void
}

/**
 * Account-facing detail. `replies` already excludes internal notes in SQL, so
 * the renderer must not filter again. There is no outstanding-ticket field on
 * this response, so F19-4 cannot be surfaced here (see the delivery notes).
 */
export function TicketDetailDrawer({ ticketId, onClose }: TicketDetailDrawerProps) {
  const query = useTicket(ticketId)
  const closeMutation = useCloseTicket()
  const [confirming, setConfirming] = useState(false)

  useEffect(() => {
    setConfirming(false)
    closeMutation.reset()
    // Reset only when the drawer switches to another ticket.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ticketId])

  const message = query.isError ? errorMessage(query.error) : null
  const hint = query.isError ? errorHint(query.error) : null
  const closeError = closeMutation.isError ? errorMessage(closeMutation.error) : null
  const closeHint = closeMutation.isError ? errorHint(closeMutation.error) : null

  return (
    <Drawer
      open={ticketId !== null}
      onClose={onClose}
      title="工单详情"
      description={query.data?.ticket_no ?? undefined}
      size="lg"
    >
      {query.isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-6 w-1/2" />
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      ) : message ? (
        <ErrorState message={message} hint={hint} onRetry={() => void query.refetch()} />
      ) : query.data ? (
        <TicketBody
          ticket={query.data}
          confirming={confirming}
          closePending={closeMutation.isPending}
          closeError={closeError}
          closeHint={closeHint}
          onStartClose={() => setConfirming(true)}
          onCancelClose={() => {
            setConfirming(false)
            closeMutation.reset()
          }}
          onConfirmClose={() =>
            closeMutation.mutate(query.data!.id, {
              onSuccess: () => setConfirming(false),
            })
          }
        />
      ) : null}
    </Drawer>
  )
}

interface TicketBodyProps {
  ticket: Ticket
  confirming: boolean
  closePending: boolean
  closeError: string | null
  closeHint: string | null
  onStartClose: () => void
  onCancelClose: () => void
  onConfirmClose: () => void
}

function TicketBody({
  ticket,
  confirming,
  closePending,
  closeError,
  closeHint,
  onStartClose,
  onCancelClose,
  onConfirmClose,
}: TicketBodyProps) {
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={stateVariant(ticket.state)}>{ticketStateLabel(ticket.state)}</Badge>
        <Badge variant="neutral">{ticketTypeLabel(ticket.ticket_type)}</Badge>
        <span className="text-xs text-ink-secondary">{ticket.sla.display}</span>
        {ticket.sla.overdue ? <Badge variant="danger">SLA 超时</Badge> : null}
      </div>

      <section aria-label="工单信息" className="space-y-2">
        <h3 className="text-sm font-semibold text-ink">{ticket.title}</h3>
        <p className="whitespace-pre-wrap break-words text-sm text-ink-secondary">
          {ticket.description}
        </p>
        <dl className="grid gap-2 text-xs sm:grid-cols-2">
          <Field label="工单号">{ticket.ticket_no}</Field>
          <Field label="创建时间">{formatDateTimeMs(ticket.created_at)}</Field>
          <Field label="首次响应截止">{formatDateTimeMs(ticket.first_response_due_at)}</Field>
          <Field label="首次响应时间">
            {ticket.first_responded_at ? formatDateTimeMs(ticket.first_responded_at) : '—'}
          </Field>
          <Field label="最近答复时间">
            {ticket.last_replied_at ? formatDateTimeMs(ticket.last_replied_at) : '—'}
          </Field>
          <Field label="重新打开次数">{formatInteger(ticket.reopen_count)}</Field>
          <Field label="关联请求">
            {ticket.request_id ? (
              <span className="break-all font-mono">{ticket.request_id}</span>
            ) : (
              '—'
            )}
          </Field>
          {ticket.ticket_type === 'CONTENT_APPEAL' ? (
            <>
              <Field label="命中规则">{ticket.policy_rule_id ?? '—'}</Field>
              <Field label="规则分类">{ticket.policy_category ?? '—'}</Field>
            </>
          ) : null}
        </dl>
      </section>

      <section aria-label="往来记录" className="space-y-2">
        <h3 className="text-sm font-semibold text-ink">往来记录</h3>
        {ticket.replies.length === 0 ? (
          <p className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-xs text-ink-secondary">
            尚无回复。运营首次答复后，这里会显示往来内容。
          </p>
        ) : (
          <ol aria-label="往来记录列表" className="space-y-2">
            {ticket.replies.map((reply) => (
              <li
                key={reply.id}
                className="rounded-lg border border-border px-3 py-2"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <Badge variant={reply.author_type === 'OPS' ? 'primary' : 'neutral'}>
                    {replyAuthorLabel(reply.author_type)}
                  </Badge>
                  <span className="text-xs text-ink-muted">
                    {formatDateTimeMs(reply.created_at)}
                  </span>
                </div>
                <p className="mt-1 whitespace-pre-wrap break-words text-sm text-ink">
                  {reply.content}
                </p>
              </li>
            ))}
          </ol>
        )}
      </section>

      {ticket.state !== 'CLOSED' ? (
        <section aria-label="关闭工单" className="space-y-2 border-t border-border pt-4">
          {confirming ? (
            <div className="space-y-2 rounded-lg border border-danger bg-danger-soft px-3 py-3">
              <p className="text-sm text-ink">关闭后不能再回复，确认关闭该工单？</p>
              {closeError ? (
                <ErrorState compact message={closeError} hint={closeHint} />
              ) : null}
              <div className="flex justify-end gap-2">
                <Button variant="secondary" size="sm" onClick={onCancelClose} disabled={closePending}>
                  取消
                </Button>
                <Button variant="danger" size="sm" loading={closePending} onClick={onConfirmClose}>
                  确认关闭
                </Button>
              </div>
            </div>
          ) : (
            <Button variant="danger" size="sm" onClick={onStartClose}>
              关闭工单
            </Button>
          )}
        </section>
      ) : null}
    </div>
  )
}
