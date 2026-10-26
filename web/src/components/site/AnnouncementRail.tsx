import type { PublicAnnouncement } from '@/api/public'
import { Badge, EmptyState, ErrorState, Skeleton } from '@/components'
import type { BadgeVariant } from '@/components'
import { formatDateTime } from '@/lib/format'
import { ANNOUNCEMENT_CATEGORY_LABELS } from '@/lib/site'

export interface AnnouncementRailProps {
  announcements: readonly PublicAnnouncement[]
  loading?: boolean
  error?: string | null
  onRetry?: () => void
}

const CATEGORY_VARIANT: Record<PublicAnnouncement['category'], BadgeVariant> = {
  PRICE: 'warning',
  RATE_LIMIT: 'info',
  GENERAL: 'neutral',
}

/** Price and rate-limit changes must be announced with an effective time. */
export function AnnouncementRail({
  announcements,
  loading = false,
  error = null,
  onRetry,
}: AnnouncementRailProps) {
  if (loading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-20 w-full" />
        <Skeleton className="h-20 w-full" />
      </div>
    )
  }

  if (error) {
    return <ErrorState compact message={error} onRetry={onRetry} />
  }

  if (announcements.length === 0) {
    return <EmptyState compact title="暂无调价公告" description="价格或限流规格调整时会在此公示。" />
  }

  return (
    <div className="flex gap-3 overflow-x-auto pb-2" role="list" aria-label="调价公告列表">
      {announcements.map((announcement) => (
        <article
          key={announcement.id}
          role="listitem"
          className="min-w-[280px] flex-1 rounded-xl border border-border bg-surface p-4 shadow-card"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={CATEGORY_VARIANT[announcement.category]}>
              {ANNOUNCEMENT_CATEGORY_LABELS[announcement.category]}
            </Badge>
            <span className="text-xs text-ink-muted">
              生效时间：{formatDateTime(announcement.effective_at)}
            </span>
          </div>
          <h3 className="mt-2 text-sm font-semibold text-ink">{announcement.title}</h3>
          <p className="mt-1 text-xs leading-relaxed text-ink-secondary">{announcement.body}</p>
        </article>
      ))}
    </div>
  )
}
