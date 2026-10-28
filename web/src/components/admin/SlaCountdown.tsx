import { useEffect, useState } from 'react'
import { cn } from '@/lib/cn'

const PAD = (value: number) => String(value).padStart(2, '0')
const MS_PER_SECOND = 1_000

/** Whole seconds -> HH:MM:SS. Hours may exceed two digits on a long overdue. */
function formatCountdown(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds))
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  const rest = seconds % 60
  return `${PAD(hours)}:${PAD(minutes)}:${PAD(rest)}`
}

export interface SlaCountdownProps {
  /** t_ticket.first_response_due_at. */
  dueAt: string
  respondedAt?: string | null
  /** CLOSED without a reply has no stopwatch left to show. */
  state?: string | null
  /** Injected clock; when supplied the component does not tick. */
  now?: Date
  tickMs?: number
  className?: string
}

/**
 * F21-2: display and alert only. There is deliberately no "auto close" or
 * "stop SLA" control here — overdue tickets stay OPEN until an operator acts.
 */
export function SlaCountdown({
  dueAt,
  respondedAt = null,
  state = null,
  now,
  tickMs = 1_000,
  className,
}: SlaCountdownProps) {
  const [current, setCurrent] = useState(() => now ?? new Date())

  useEffect(() => {
    if (now) {
      setCurrent(now)
      return
    }
    setCurrent(new Date())
    const timer = setInterval(() => setCurrent(new Date()), tickMs)
    return () => clearInterval(timer)
  }, [now, tickMs])

  if (respondedAt) {
    return (
      <span
        data-testid="sla-countdown"
        className={cn('text-xs text-ink-secondary', className)}
      >
        已响应
      </span>
    )
  }

  if (state === 'CLOSED') {
    return (
      <span
        data-testid="sla-countdown"
        className={cn('text-xs text-ink-muted', className)}
      >
        已关闭（未响应）
      </span>
    )
  }

  const due = new Date(dueAt).getTime()
  if (Number.isNaN(due)) {
    return (
      <span data-testid="sla-countdown" className={cn('text-xs text-ink-muted', className)}>
        —
      </span>
    )
  }

  const diffSeconds = Math.floor((due - current.getTime()) / MS_PER_SECOND)
  if (diffSeconds <= 0) {
    return (
      <span
        data-testid="sla-countdown"
        className={cn('font-medium tabular-nums text-danger', className)}
      >
        已超时 {formatCountdown(Math.abs(diffSeconds))}
      </span>
    )
  }

  return (
    <span
      data-testid="sla-countdown"
      className={cn('tabular-nums text-ink-secondary', className)}
    >
      剩余 {formatCountdown(diffSeconds)}
    </span>
  )
}
