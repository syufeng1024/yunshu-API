import { cn } from '@/lib/cn'

export interface SkeletonProps {
  className?: string
}

export function Skeleton({ className }: SkeletonProps) {
  return (
    <div
      className={cn('animate-pulse rounded-md bg-surface-muted', className)}
      aria-hidden="true"
    />
  )
}

export interface SkeletonTextProps {
  lines?: number
  className?: string
}

export function SkeletonText({ lines = 3, className }: SkeletonTextProps) {
  return (
    <div className={cn('space-y-2', className)}>
      {Array.from({ length: lines }, (_, index) => (
        <Skeleton
          key={index}
          className={index === lines - 1 ? 'h-4 w-2/3' : 'h-4 w-full'}
        />
      ))}
    </div>
  )
}
