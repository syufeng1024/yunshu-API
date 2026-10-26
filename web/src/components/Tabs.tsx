import { useId, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode } from 'react'
import { cn } from '@/lib/cn'

export interface TabItem {
  id: string
  label: string
  content: ReactNode
  disabled?: boolean
}

export interface TabsProps {
  items: readonly TabItem[]
  value?: string
  defaultValue?: string
  onChange?: (id: string) => void
  ariaLabel?: string
  className?: string
}

export function Tabs({ items, value, defaultValue, onChange, ariaLabel, className }: TabsProps) {
  const prefix = useId()
  const enabledItems = items.filter((item) => !item.disabled)
  const fallback = defaultValue ?? enabledItems[0]?.id ?? ''
  const [internalValue, setInternalValue] = useState(fallback)
  const selectedId = value ?? internalValue
  const activeItem = items.find((item) => item.id === selectedId) ?? enabledItems[0]
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([])

  const select = (id: string) => {
    if (value === undefined) setInternalValue(id)
    onChange?.(id)
  }

  const move = (direction: 1 | -1) => {
    if (enabledItems.length === 0) return
    const currentIndex = enabledItems.findIndex((item) => item.id === activeItem?.id)
    const nextIndex = (currentIndex + direction + enabledItems.length) % enabledItems.length
    const next = enabledItems[nextIndex]
    if (!next) return
    select(next.id)
    const sourceIndex = items.findIndex((item) => item.id === next.id)
    tabRefs.current[sourceIndex]?.focus()
  }

  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ArrowRight') {
      event.preventDefault()
      move(1)
    } else if (event.key === 'ArrowLeft') {
      event.preventDefault()
      move(-1)
    } else if (event.key === 'Home') {
      event.preventDefault()
      const first = enabledItems[0]
      if (first) {
        select(first.id)
        tabRefs.current[items.findIndex((item) => item.id === first.id)]?.focus()
      }
    } else if (event.key === 'End') {
      event.preventDefault()
      const last = enabledItems[enabledItems.length - 1]
      if (last) {
        select(last.id)
        tabRefs.current[items.findIndex((item) => item.id === last.id)]?.focus()
      }
    }
  }

  return (
    <div className={className}>
      <div
        role="tablist"
        aria-label={ariaLabel}
        className="flex gap-1 overflow-x-auto border-b border-border"
      >
        {items.map((item, index) => {
          const selected = item.id === activeItem?.id
          return (
            <button
              key={item.id}
              ref={(node) => {
                tabRefs.current[index] = node
              }}
              type="button"
              role="tab"
              id={`${prefix}-tab-${item.id}`}
              aria-selected={selected}
              aria-controls={`${prefix}-panel-${item.id}`}
              tabIndex={selected ? 0 : -1}
              disabled={item.disabled}
              onClick={() => select(item.id)}
              onKeyDown={handleKeyDown}
              className={cn(
                'shrink-0 border-b-2 px-3 py-2 text-sm font-medium transition-colors duration-fast focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
                selected
                  ? 'border-primary text-primary'
                  : 'border-transparent text-ink-secondary hover:text-ink',
                item.disabled && 'cursor-not-allowed opacity-50',
              )}
            >
              {item.label}
            </button>
          )
        })}
      </div>
      {activeItem ? (
        <div
          key={activeItem.id}
          role="tabpanel"
          id={`${prefix}-panel-${activeItem.id}`}
          aria-labelledby={`${prefix}-tab-${activeItem.id}`}
          tabIndex={0}
          className="pt-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
        >
          {activeItem.content}
        </div>
      ) : null}
    </div>
  )
}
