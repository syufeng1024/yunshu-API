import { useEffect } from 'react'
import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Skeleton } from '@/components/Skeleton'
import { ROUTES, isAdminRole } from '@/lib/constants'
import { usePageTitle } from '@/lib/usePageTitle'
import { useAuthStore } from '@/stores/auth'

export function RouteLoading({ fullScreen = false }: { fullScreen?: boolean }) {
  return (
    <div
      className={
        fullScreen
          ? 'flex min-h-screen items-center justify-center bg-canvas p-6'
          : 'flex min-h-48 items-center justify-center p-6'
      }
      aria-busy="true"
      aria-label="页面加载中"
    >
      <div className="w-full max-w-sm space-y-3">
        <Skeleton className="h-6 w-40" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-2/3" />
      </div>
    </div>
  )
}

export function ForbiddenPage() {
  usePageTitle('无权限')
  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas p-6">
      <div className="w-full max-w-md space-y-4">
        <EmptyState
          title="403 · 无权限"
          description="当前账号无权访问该区域。前端菜单隐藏只做体验，服务端仍会对每个接口校验权限。"
          icon={<span className="text-sm font-semibold text-ink-secondary">403</span>}
        />
        <p className="text-center text-xs text-ink-muted">
          运营后台仅对运营、财务与超级管理员开放。
        </p>
      </div>
    </div>
  )
}

export interface RequireAuthProps {
  role: 'user' | 'admin'
  children: ReactNode
}

/**
 * UX-only route guard. It redirects anonymous visitors to /login and renders
 * a 403 shell for the wrong role; every page must still validate permissions
 * against the backend when its data loads (PRD §1.3, hard constraint 8).
 */
export function RequireAuth({ role, children }: RequireAuthProps) {
  const location = useLocation()
  const status = useAuthStore((state) => state.status)
  const user = useAuthStore((state) => state.user)
  const error = useAuthStore((state) => state.error)
  const hint = useAuthStore((state) => state.hint)
  const bootstrap = useAuthStore((state) => state.bootstrap)

  useEffect(() => {
    if (status === 'unknown') {
      void bootstrap()
    }
  }, [bootstrap, status])

  if (status === 'unknown' || status === 'loading') {
    return <RouteLoading fullScreen />
  }

  if (status === 'error') {
    return (
      <div className="flex min-h-screen items-center justify-center bg-canvas p-6">
        <div className="w-full max-w-md">
          <ErrorState
            message={error ?? '登录状态校验失败，请重试。'}
            hint={hint}
            onRetry={() => {
              void bootstrap()
            }}
          />
        </div>
      </div>
    )
  }

  if (status === 'anonymous') {
    const next = encodeURIComponent(`${location.pathname}${location.search}`)
    return <Navigate to={`${ROUTES.login}?next=${next}`} replace />
  }

  if (role === 'admin' && !isAdminRole(user?.role)) {
    return <ForbiddenPage />
  }

  return <>{children}</>
}
