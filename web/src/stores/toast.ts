import { create } from 'zustand'

export type ToastVariant = 'success' | 'error' | 'info'

export interface ToastInput {
  title: string
  description?: string
  variant?: ToastVariant
  durationMs?: number
}

export interface ToastItem extends Required<Omit<ToastInput, 'description'>> {
  id: string
  description: string
}

interface ToastState {
  toasts: ToastItem[]
  push: (input: ToastInput) => string
  dismiss: (id: string) => void
}

let sequence = 0
const timers = new Map<string, ReturnType<typeof setTimeout>>()

export const useToastStore = create<ToastState>((set) => ({
  toasts: [],
  push: (input) => {
    sequence += 1
    const id = `toast-${sequence}`
    const item: ToastItem = {
      id,
      title: input.title,
      description: input.description ?? '',
      variant: input.variant ?? 'info',
      durationMs: input.durationMs ?? 4_000,
    }
    set((state) => ({ toasts: [...state.toasts, item].slice(-4) }))
    const timer = setTimeout(() => {
      timers.delete(id)
      set((state) => ({ toasts: state.toasts.filter((toast) => toast.id !== id) }))
    }, item.durationMs)
    timers.set(id, timer)
    return id
  },
  dismiss: (id) => {
    const timer = timers.get(id)
    if (timer) {
      clearTimeout(timer)
      timers.delete(id)
    }
    set((state) => ({ toasts: state.toasts.filter((toast) => toast.id !== id) }))
  },
}))
