import { create } from 'zustand'
import { api, ApiError, errorHint, errorMessage } from '@/api/client'
import type { components } from '@/api/types'

export type CurrentUser = components['schemas']['CurrentUser']

export type AuthStatus = 'unknown' | 'loading' | 'authenticated' | 'anonymous' | 'error'

interface AuthState {
  status: AuthStatus
  user: CurrentUser | null
  error: string | null
  hint: string | null
  bootstrap: () => Promise<void>
  logout: () => Promise<void>
  clear: () => void
}

export const useAuthStore = create<AuthState>((set, get) => ({
  status: 'unknown',
  user: null,
  error: null,
  hint: null,
  bootstrap: async () => {
    if (get().status === 'loading') return
    set({ status: 'loading', error: null, hint: null })
    try {
      const user = await api.get<CurrentUser>('/api/v1/auth/me', { redirectOn401: false })
      set({ status: 'authenticated', user, error: null, hint: null })
    } catch (error) {
      if (error instanceof ApiError && error.status === 401) {
        set({ status: 'anonymous', user: null, error: null, hint: null })
        return
      }
      set({
        status: 'error',
        user: null,
        error: errorMessage(error),
        hint: errorHint(error),
      })
    }
  },
  logout: async () => {
    // TODO(T18): surface server-side session revocation failures once the auth
    // endpoints land. Local state is cleared regardless so the UI cannot keep
    // showing a stale authenticated shell.
    try {
      await api.post<void>('/api/v1/auth/logout')
    } catch {
      // Intentionally ignored; the local session is discarded below.
    }
    set({ status: 'anonymous', user: null, error: null, hint: null })
  },
  clear: () => set({ status: 'anonymous', user: null, error: null, hint: null }),
}))
