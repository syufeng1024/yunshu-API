import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'
import { Icon } from './Icon'

export interface EmptyStateProps {
  title: string
  description?: string
  action?: ReactNode
  icon?: ReactNode
  compact?: boolean
  className?: string
}

export function EmptyState({
  title,
  description,
  action,
  icon,
  compact = false,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center justify-center rounded-lg border border-dashed border-border-strong bg-surface text-center',
        compact ? 'gap-2 px-4 py-6' : 'gap-3 px-6 py-12',
        className,
      )}
      role="status"
    >
      <div className="flex h-10 w-10 items-center justify-center rounded-full bg-surface-muted text-ink-secondary">
        {icon ?? <Icon name="grid" className="h-5 w-5" />}
      </div>
      <div className="space-y-1">
        <p className="text-sm font-medium text-ink">{title}</p>
        {description ? <p className="text-xs text-ink-muted">{description}</p> : null}
      </div>
      {action}
    </div>
  )
}
