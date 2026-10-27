import { Icon } from '../Icon'

export interface Announcement {
  id: string
  title: string
  body: string
  published_at: string
}

export interface AnnouncementSlotProps {
  /**
   * No console announcement endpoint exists yet (the ops side owns
   * /api/admin/announcements). The default is an empty list, and the slot
   * renders an empty state instead of sample copy or a hidden element.
   */
  items?: readonly Announcement[]
}

/** Announcement strip on the overview (PRD §5.1). */
export function AnnouncementSlot({ items = [] }: AnnouncementSlotProps) {
  return (
    <section
      aria-label="运营公告"
      className="flex items-start gap-3 rounded-lg border border-border bg-surface px-4 py-3"
    >
      <Icon name="megaphone" className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
      <div className="min-w-0 flex-1">
        {items.length === 0 ? (
          <p className="text-xs text-ink-muted">暂无公告。运营发布后在此展示。</p>
        ) : (
          <ul className="space-y-1">
            {items.map((item) => (
              <li key={item.id} className="text-xs text-ink-secondary">
                <span className="font-medium text-ink">{item.title}</span>
                <span className="ml-2 text-ink-muted">{item.body}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}
