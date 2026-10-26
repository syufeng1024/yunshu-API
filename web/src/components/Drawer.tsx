import { useId } from 'react'
import { createPortal } from 'react-dom'
import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'
import { useFocusTrap } from '@/lib/hooks/useFocusTrap'
import { Button } from './Button'

export interface DrawerProps {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  children: ReactNode
  footer?: ReactNode
  className?: string
}

export function Drawer({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  className,
}: DrawerProps) {
  const titleId = useId()
  const descriptionId = useId()
  const containerRef = useFocusTrap<HTMLDivElement>(open, onClose)

  if (!open) return null

  return createPortal(
    <div className="fixed inset-0 z-40">
      <div className="absolute inset-0 bg-overlay" onClick={onClose} aria-hidden="true" />
      <div
        ref={containerRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        className={cn(
          'absolute inset-y-0 right-0 flex w-full max-w-md flex-col overflow-hidden bg-surface shadow-overlay',
          className,
        )}
      >
        <header className="flex items-start justify-between gap-4 border-b border-border px-5 py-4">
          <div className="space-y-1">
            <h2 id={titleId} className="text-base font-semibold text-ink">
              {title}
            </h2>
            {description ? (
              <p id={descriptionId} className="text-xs text-ink-secondary">
                {description}
              </p>
            ) : null}
          </div>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="关闭抽屉">
            关闭
          </Button>
        </header>
        <div className="flex-1 overflow-y-auto px-5 py-5">{children}</div>
        {footer ? (
          <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-border px-5 py-4">
            {footer}
          </footer>
        ) : null}
      </div>
    </div>,
    document.body,
  )
}
