import type { ReactNode } from 'react'
import type { BadgeVariant } from '@/components'
import { Badge } from '@/components'
import type { LogAttempt, LogDetailRequest } from '@/api/console'
import {
  attemptPhaseLabel,
  attemptResultLabel,
  failOwnerLabel,
  failStageLabel,
  retryDecisionLabel,
} from '@/lib/consoleLabels'
import { formatDateTimeMs, formatDurationMs, formatInteger } from '@/lib/format'

export interface LogTimelineProps {
  attempts: readonly LogAttempt[]
  request: LogDetailRequest
}

function attemptVariant(result: string): BadgeVariant {
  if (result === 'SUCCESS') return 'success'
  if (result === 'ABORTED') return 'warning'
  return 'danger'
}

function metric(value: number | null | undefined): string {
  return value === null || value === undefined ? '—' : formatDurationMs(value)
}

function text(value: string | null | undefined): string {
  return value && value.length > 0 ? value : '—'
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-ink-muted">{label}</dt>
      <dd className="break-words text-ink">{children}</dd>
    </div>
  )
}

/**
 * One row per upstream attempt. The backend returns `timeline` ordered by
 * attempt_index, so a request that retried once renders two rows (F11-2).
 */
export function LogTimeline({ attempts, request }: LogTimelineProps) {
  if (attempts.length === 0) {
    const stage = failStageLabel(request.fail_stage)
    const owner = failOwnerLabel(request.fail_owner)
    return (
      <section aria-label="重试时间线" className="space-y-2">
        <h3 className="text-sm font-semibold text-ink">重试时间线</h3>
        <p className="rounded-lg border border-border bg-surface-muted px-3 py-2 text-xs text-ink-secondary">
          该请求未产生上游尝试。
          {stage ? ` 失败阶段：${stage}。` : ''}
          {owner ? ` 失败归属：${owner}。` : ''}
        </p>
      </section>
    )
  }

  return (
    <section aria-label="重试时间线" className="space-y-2">
      <h3 className="text-sm font-semibold text-ink">重试时间线</h3>
      <ol aria-label="重试时间线" className="space-y-2">
        {attempts.map((attempt) => (
          <li
            key={`${attempt.attempt_index}-${attempt.created_at}`}
            className="rounded-lg border border-border px-3 py-3"
          >
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm font-medium text-ink">
                第 {attempt.attempt_index} 次尝试
              </span>
              <Badge variant={attemptVariant(attempt.result)}>
                {attemptResultLabel(attempt.result)}
              </Badge>
              <span className="text-xs tabular-nums text-ink-secondary">
                HTTP {attempt.http_status ?? '—'}
              </span>
              <span className="text-xs text-ink-muted">
                {attemptPhaseLabel(attempt.phase)}
              </span>
            </div>
            <dl className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs sm:grid-cols-3">
              <Field label="连接耗时">{metric(attempt.connect_ms)}</Field>
              <Field label="首字节耗时">{metric(attempt.first_byte_ms)}</Field>
              <Field label="总耗时">{metric(attempt.total_ms)}</Field>
              <Field label="渠道">{text(attempt.channel_name)}</Field>
              <Field label="上游模型">{text(attempt.upstream_model)}</Field>
              <Field label="重试决策">
                {retryDecisionLabel(attempt.retry_decision) ?? '—'}
              </Field>
              <Field label="错误类别">{text(attempt.error_class)}</Field>
              <Field label="上游错误码">{text(attempt.upstream_code)}</Field>
              <Field label="Token 入 / 出">
                {formatInteger(attempt.tokens_in)} / {formatInteger(attempt.tokens_out)}
              </Field>
              <Field label="发生时间">{formatDateTimeMs(attempt.created_at)}</Field>
            </dl>
            {attempt.upstream_msg ? (
              <p className="mt-2 break-words rounded-md bg-danger-soft px-2 py-1 text-xs text-danger">
                {attempt.upstream_msg}
              </p>
            ) : null}
          </li>
        ))}
      </ol>
    </section>
  )
}
