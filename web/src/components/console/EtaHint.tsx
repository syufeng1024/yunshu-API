import type { EtaReason, OverviewEta } from '@/api/console'
import { cn } from '@/lib/cn'
import { formatCreditAmount, formatDateTime, formatDurationSeconds } from '@/lib/format'

export interface EtaHintProps {
  eta: OverviewEta | null | undefined
  className?: string
}

interface EtaCopy {
  tone: 'ok' | 'warning' | 'danger' | 'muted'
  text: string
  detail?: string
}

const TONE: Record<EtaCopy['tone'], string> = {
  ok: 'border-border bg-surface-muted text-ink-secondary',
  warning: 'border-warning bg-warning-soft text-warning',
  danger: 'border-danger bg-danger-soft text-danger',
  muted: 'border-border bg-surface-muted text-ink-muted',
}

function secondsText(seconds: number | null): string | null {
  if (seconds === null || !Number.isFinite(seconds) || seconds < 0) return null
  return formatDurationSeconds(seconds)
}

/**
 * Turns the backend ETA into readable copy. NO_DAILY_QUOTA is deliberately a
 * distinct sentence from a zero: an unlimited plan must not look like "0 h
 * left", and a missing quota must not look like "触顶".
 */
export function describeEta(eta: OverviewEta | null | undefined): EtaCopy {
  if (!eta) return { tone: 'muted', text: '触顶预测不可用' }

  const burn =
    eta.burn_micro_per_hour > 0
      ? `近 1 小时消耗 ${formatCreditAmount(eta.burn_micro_per_hour)} 积分/小时`
      : undefined
  const reason = eta.reason as EtaReason

  if (reason === 'NO_DAILY_QUOTA') {
    return { tone: 'muted', text: '无日额度上限', detail: '当前套餐不设日额度，不会因日额度触顶被拒。' }
  }
  if (reason === 'QUOTA_REACHED') {
    return { tone: 'danger', text: '今日额度已触顶', detail: '后续调用将返回 402，请等待次日重置或申请提额。' }
  }
  if (reason === 'NO_RECENT_USAGE') {
    return { tone: 'muted', text: '近 1 小时无消耗，暂无法预测触顶时间', detail: burn }
  }
  if (reason === 'OK') {
    const left = secondsText(eta.seconds_left)
    if (left && eta.eta_at) {
      return {
        tone: 'warning',
        text: `按当前速度约 ${left} 后触顶（${formatDateTime(eta.eta_at)}）`,
        detail: burn,
      }
    }
    return { tone: 'warning', text: '按当前速度将触顶', detail: burn }
  }
  return { tone: 'muted', text: '触顶预测不可用', detail: burn }
}

export function EtaHint({ eta, className }: EtaHintProps) {
  const copy = describeEta(eta)
  return (
    <div className={cn('rounded-lg border px-3 py-2 text-xs', TONE[copy.tone], className)}>
      <p className="font-medium">{copy.text}</p>
      {copy.detail ? <p className="mt-0.5 opacity-80">{copy.detail}</p> : null}
    </div>
  )
}
