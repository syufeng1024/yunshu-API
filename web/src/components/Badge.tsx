import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'

export type BadgeVariant = 'neutral' | 'primary' | 'success' | 'warning' | 'danger' | 'info'

const VARIANTS: Record<BadgeVariant, string> = {
  neutral: 'border-border-strong bg-surface-muted text-ink-secondary',
  primary: 'border-primary-ring bg-primary-soft text-primary-strong',
  success: 'border-success bg-success-soft text-success',
  warning: 'border-warning bg-warning-soft text-warning',
  danger: 'border-danger bg-danger-soft text-danger',
  info: 'border-info bg-info-soft text-info',
}

export interface BadgeProps {
  variant?: BadgeVariant
  dot?: boolean
  children: ReactNode
  className?: string
}

export function Badge({ variant = 'neutral', dot = false, children, className }: BadgeProps) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium',
        VARIANTS[variant],
        className,
      )}
    >
      {dot ? <span className="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true" /> : null}
      {children}
    </span>
  )
}
