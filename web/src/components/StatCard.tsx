import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'
import { ErrorState } from './ErrorState'
import { Skeleton } from './Skeleton'

export interface StatDelta {
  text: string
  tone?: 'positive' | 'negative' | 'neutral'
}

export interface StatCardProps {
  label: string
  value?: ReactNode
  hint?: string
  delta?: StatDelta
  loading?: boolean
  error?: string | null
  onRetry?: () => void
  icon?: ReactNode
  className?: string
}

const DELTA_TONE: Record<NonNullable<StatDelta['tone']>, string> = {
  positive: 'text-success',
  negative: 'text-danger',
  neutral: 'text-ink-muted',
}

export function StatCard({
  label,
  value,
  hint,
  delta,
  loading = false,
  error = null,
  onRetry,
  icon,
  className,
}: StatCardProps) {
  return (
    <div
      className={cn('rounded-xl border border-border bg-surface p-5 shadow-card', className)}
      aria-busy={loading || undefined}
    >
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs font-medium text-ink-secondary">{label}</p>
        {icon ? <span className="text-ink-muted">{icon}</span> : null}
      </div>

      {loading ? (
        <Skeleton className="mt-3 h-8 w-28" />
      ) : error ? (
        <ErrorState compact message={error} onRetry={onRetry} className="mt-3" />
      ) : (
        <p className="mt-2 text-2xl font-semibold tracking-tight text-ink">{value ?? '—'}</p>
      )}

      {!loading && !error && delta ? (
        <p className={cn('mt-1 text-xs font-medium', DELTA_TONE[delta.tone ?? 'neutral'])}>
          {delta.text}
        </p>
      ) : null}
      {!loading && !error && hint ? <p className="mt-1 text-xs text-ink-muted">{hint}</p> : null}
    </div>
  )
}
