import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { Badge } from '@/components/Badge'
import { Icon } from '@/components/Icon'
import { Logo } from '@/components/Logo'
import { ScrollRestorer } from '@/components/ScrollRestorer'
import { ADMIN_NAV, ROUTES, USER_ROLE_LABEL } from '@/lib/constants'
import { cn } from '@/lib/cn'
import { useAuthStore } from '@/stores/auth'

function navClass({ isActive }: { isActive: boolean }): string {
  return cn(
    'flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors duration-fast focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
    isActive
      ? 'bg-sidebar-active text-sidebar-text'
      : 'text-sidebar-muted hover:bg-sidebar-hover hover:text-sidebar-text',
  )
}

export function AdminLayout() {
  const user = useAuthStore((state) => state.user)
  const logout = useAuthStore((state) => state.logout)
  const navigate = useNavigate()
  const visibleNav = ADMIN_NAV.filter((item) => user !== null && item.roles.includes(user.role))

  const handleLogout = async () => {
    await logout()
    navigate(ROUTES.login, { replace: true })
  }

  return (
    <div className="min-h-screen bg-canvas md:flex">
      <ScrollRestorer />
      <aside className="sticky top-0 z-30 flex w-full shrink-0 flex-row items-center gap-2 bg-sidebar-bg px-3 py-2 md:h-screen md:w-sidebar md:flex-col md:items-stretch md:gap-5 md:px-4 md:py-5">
        <div className="flex items-center justify-between gap-2 md:justify-start">
          <Logo variant="dark" size="sm" collapsible to={ROUTES.admin} />
          {user ? (
            <span className="hidden md:inline-flex">
              <Badge variant="info">{USER_ROLE_LABEL[user.role]}</Badge>
            </span>
          ) : null}
        </div>

        <nav
          aria-label="运营后台导航"
          className="flex flex-1 items-center gap-1 overflow-x-auto md:mt-1 md:flex-col md:items-stretch md:gap-1 md:overflow-visible"
        >
          {visibleNav.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className={navClass}
              title={item.label}
              aria-label={item.label}
            >
              <Icon name={item.icon} className="h-5 w-5" />
              <span className="hidden whitespace-nowrap md:inline">{item.label}</span>
            </NavLink>
          ))}
        </nav>

        <button
          type="button"
          onClick={handleLogout}
          className="inline-flex items-center gap-2 rounded-md px-2 py-2 text-xs text-sidebar-muted transition-colors duration-fast hover:bg-sidebar-hover hover:text-sidebar-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary md:hidden"
          aria-label="退出登录"
        >
          <Icon name="logout" className="h-5 w-5" />
        </button>

        <div className="mt-auto hidden border-t border-sidebar-border pt-4 md:block">
          <p className="truncate text-xs text-sidebar-muted">{user?.email ?? ''}</p>
          <button
            type="button"
            onClick={handleLogout}
            className="mt-2 inline-flex items-center gap-2 rounded-md px-1 py-1 text-xs text-sidebar-muted transition-colors duration-fast hover:text-sidebar-text focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
          >
            <Icon name="logout" className="h-4 w-4" />
            退出登录
          </button>
        </div>
      </aside>

      <div className="min-w-0 flex-1">
        <main className="mx-auto w-full max-w-6xl px-4 py-6 sm:px-6 lg:px-8">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
