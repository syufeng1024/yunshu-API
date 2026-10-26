import { Link, NavLink, Outlet } from 'react-router-dom'
import { buttonVariants } from '@/components/Button'
import { Copyright } from '@/components/layout-parts'
import { Logo } from '@/components/Logo'
import { BRAND_DESCRIPTION, ROUTES, SITE_NAV } from '@/lib/constants'
import { cn } from '@/lib/cn'

function navLinkClass({ isActive }: { isActive: boolean }): string {
  return cn(
    'rounded-md px-3 py-2 text-sm font-medium transition-colors duration-fast',
    isActive
      ? 'bg-primary-soft text-primary'
      : 'text-ink-secondary hover:bg-surface-muted hover:text-ink',
  )
}

export function SiteLayout() {
  return (
    <div className="flex min-h-screen flex-col bg-canvas">
      <a href="#main-content" className="skip-link">
        跳到主内容
      </a>

      <header className="sticky top-0 z-30 border-b border-border bg-surface">
        <div className="mx-auto flex h-16 w-full max-w-6xl items-center justify-between gap-4 px-4 sm:px-6">
          <Logo />

          <nav aria-label="主导航" className="hidden items-center gap-1 md:flex">
            {SITE_NAV.map((item) => (
              <NavLink key={item.to} to={item.to} end={item.end} className={navLinkClass}>
                {item.label}
              </NavLink>
            ))}
          </nav>

          <div className="flex items-center gap-2">
            <Link to={ROUTES.login} className={buttonVariants({ variant: 'ghost', size: 'sm' })}>
              登录
            </Link>
            <Link to={ROUTES.login} className={buttonVariants({ variant: 'primary', size: 'sm' })}>
              免费开始
            </Link>
          </div>
        </div>

        <nav
          aria-label="移动端导航"
          className="flex gap-1 overflow-x-auto border-t border-border px-4 py-2 md:hidden"
        >
          {SITE_NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.end}
              className={navLinkClass}
            >
              {item.label}
            </NavLink>
          ))}
        </nav>
      </header>

      <main id="main-content" className="flex-1">
        <Outlet />
      </main>

      <footer className="site-footer border-t border-border bg-surface">
        <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 px-4 py-8 sm:px-6 md:flex-row md:items-center md:justify-between">
          <div className="space-y-2">
            <Logo />
            <p className="text-xs text-ink-muted">{BRAND_DESCRIPTION}</p>
          </div>

          <nav aria-label="页脚导航" className="flex flex-wrap gap-x-5 gap-y-2 text-sm">
            <Link to={ROUTES.pricing} className="text-ink-secondary hover:text-ink">
              定价
            </Link>
            <Link to={ROUTES.docs} className="text-ink-secondary hover:text-ink">
              文档
            </Link>
            <Link to={ROUTES.integrations} className="text-ink-secondary hover:text-ink">
              工具集成
            </Link>
            <Link to={ROUTES.login} className="text-ink-secondary hover:text-ink">
              登录
            </Link>
          </nav>

          <Copyright />
        </div>
      </footer>
    </div>
  )
}
