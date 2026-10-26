import { cloneElement, isValidElement, useEffect, useId, useRef, useState } from 'react'
import type { ReactElement, ReactNode } from 'react'
import { cn } from '@/lib/cn'

export interface TooltipProps {
  content: ReactNode
  children: ReactNode
  side?: 'top' | 'bottom'
  delayMs?: number
  className?: string
}

export function Tooltip({ content, children, side = 'top', delayMs = 150, className }: TooltipProps) {
  const [visible, setVisible] = useState(false)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const tooltipId = useId()

  useEffect(() => {
    return () => {
      if (timerRef.current) clearTimeout(timerRef.current)
    }
  }, [])

  const show = () => {
    if (timerRef.current) clearTimeout(timerRef.current)
    timerRef.current = setTimeout(() => setVisible(true), delayMs)
  }

  const hide = () => {
    if (timerRef.current) clearTimeout(timerRef.current)
    setVisible(false)
  }

  const trigger = isValidElement(children)
    ? cloneElement(children as ReactElement<{ 'aria-describedby'?: string }>, {
        'aria-describedby': visible ? tooltipId : undefined,
      })
    : children

  return (
    <span
      className={cn('relative inline-flex', className)}
      onMouseEnter={show}
      onMouseLeave={hide}
      onFocus={show}
      onBlur={hide}
    >
      {trigger}
      {visible ? (
        <span
          role="tooltip"
          id={tooltipId}
          className={cn(
            'absolute left-1/2 z-50 w-max max-w-xs -translate-x-1/2 rounded-md bg-ink px-2 py-1 text-xs text-ink-inverse shadow-pop',
            side === 'top' ? 'bottom-full mb-2' : 'top-full mt-2',
          )}
        >
          {content}
        </span>
      ) : null}
    </span>
  )
}
