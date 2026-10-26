import { cn } from '@/lib/cn'
import { Button } from './Button'
import { Icon } from './Icon'

export interface ErrorStateProps {
  title?: string
  message: string
  hint?: string | null
  onRetry?: () => void
  compact?: boolean
  className?: string
}

/**
 * Failure UI shared by pages, tables and charts. A failed request must render
 * this instead of an empty table or a fabricated zero value.
 */
export function ErrorState({
  title = '加载失败',
  message,
  hint,
  onRetry,
  compact = false,
  className,
}: ErrorStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center rounded-lg border border-danger bg-danger-soft text-center',
        compact ? 'gap-2 px-4 py-6' : 'gap-3 px-6 py-12',
        className,
      )}
      role="alert"
    >
      <div className="flex h-10 w-10 items-center justify-center rounded-full bg-surface text-danger">
        <Icon name="alert" className="h-5 w-5" />
      </div>
      <div className="space-y-1">
        <p className="text-sm font-medium text-ink">{title}</p>
        <p className="text-xs text-ink-secondary">{message}</p>
        {hint ? <p className="text-xs text-ink-muted">{hint}</p> : null}
      </div>
      {onRetry ? (
        <Button variant="secondary" size="sm" onClick={onRetry}>
          重试
        </Button>
      ) : null}
    </div>
  )
}
