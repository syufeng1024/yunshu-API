import { useId } from 'react'
import { createPortal } from 'react-dom'
import type { ReactNode } from 'react'
import { cn } from '@/lib/cn'
import { useFocusTrap } from '@/lib/hooks/useFocusTrap'
import { Button } from './Button'

export interface ModalProps {
  open: boolean
  onClose: () => void
  title: string
  description?: string
  children: ReactNode
  footer?: ReactNode
  size?: 'sm' | 'md' | 'lg'
  className?: string
}

const SIZE: Record<NonNullable<ModalProps['size']>, string> = {
  sm: 'max-w-sm',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
}

export function Modal({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  size = 'md',
  className,
}: ModalProps) {
  const titleId = useId()
  const descriptionId = useId()
  const containerRef = useFocusTrap<HTMLDivElement>(open, onClose)

  if (!open) return null

  return createPortal(
    <div className="fixed inset-0 z-40 flex items-center justify-center p-4">
      <div className="absolute inset-0 bg-overlay" onClick={onClose} aria-hidden="true" />
      <div
        ref={containerRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
        className={cn(
          'relative flex max-h-full w-full flex-col overflow-hidden rounded-xl bg-surface shadow-overlay',
          SIZE[size],
          className,
        )}
      >
        <header className="flex items-start justify-between gap-4 border-b border-border px-6 py-4">
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
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="关闭弹窗">
            关闭
          </Button>
        </header>
        <div className="overflow-y-auto px-6 py-5">{children}</div>
        {footer ? (
          <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-border px-6 py-4">
            {footer}
          </footer>
        ) : null}
      </div>
    </div>,
    document.body,
  )
}
