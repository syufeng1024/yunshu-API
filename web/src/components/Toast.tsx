import { useMemo } from 'react'
import { createPortal } from 'react-dom'
import { cn } from '@/lib/cn'
import { useToastStore } from '@/stores/toast'
import type { ToastInput, ToastVariant } from '@/stores/toast'
import { Icon } from './Icon'

const VARIANT: Record<ToastVariant, { card: string; icon: string }> = {
  success: { card: 'border-success bg-success-soft', icon: 'text-success' },
  error: { card: 'border-danger bg-danger-soft', icon: 'text-danger' },
  info: { card: 'border-info bg-info-soft', icon: 'text-info' },
}

export function ToastViewport() {
  const toasts = useToastStore((state) => state.toasts)
  const dismiss = useToastStore((state) => state.dismiss)

  if (typeof document === 'undefined') return null

  return createPortal(
    <div
      className="pointer-events-none fixed inset-x-4 bottom-4 z-50 flex flex-col items-stretch gap-2 sm:left-auto sm:right-6 sm:w-full sm:max-w-sm"
      aria-live="polite"
      aria-atomic="false"
    >
      {toasts.map((toast) => {
        const variant = VARIANT[toast.variant]
        return (
          <div
            key={toast.id}
            role={toast.variant === 'error' ? 'alert' : 'status'}
            className={cn(
              'pointer-events-auto flex items-start gap-3 rounded-xl border p-4 shadow-pop',
              variant.card,
            )}
          >
            <Icon
              name={toast.variant === 'success' ? 'check' : 'alert'}
              className={cn('mt-0.5 h-4 w-4', variant.icon)}
            />
            <div className="min-w-0 flex-1 space-y-0.5">
              <p className="text-sm font-medium text-ink">{toast.title}</p>
              {toast.description ? (
                <p className="text-xs text-ink-secondary">{toast.description}</p>
              ) : null}
            </div>
            <button
              type="button"
              onClick={() => dismiss(toast.id)}
              aria-label="关闭提示"
              className="rounded-sm text-xs text-ink-muted transition-colors duration-fast hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
            >
              关闭
            </button>
          </div>
        )
      })}
    </div>,
    document.body,
  )
}

export interface ToastApi {
  show: (input: ToastInput) => string
  success: (title: string, description?: string) => string
  error: (title: string, description?: string) => string
  info: (title: string, description?: string) => string
}

export function useToast(): ToastApi {
  const push = useToastStore((state) => state.push)
  return useMemo(
    () => ({
      show: push,
      success: (title, description) => push({ title, description, variant: 'success' }),
      error: (title, description) => push({ title, description, variant: 'error' }),
      info: (title, description) => push({ title, description, variant: 'info' }),
    }),
    [push],
  )
}
