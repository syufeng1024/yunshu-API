import type { ReactNode } from 'react'
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

function FooterGroup({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div>
      <h2 className="text-sm font-semibold text-ink">{title}</h2>
      <ul className="mt-3 space-y-2 text-sm text-ink-secondary">{children}</ul>
    </div>
  )
}

const PRODUCT_LINKS = [
  { label: '定价', to: ROUTES.pricing },
  { label: '工具集成', to: ROUTES.integrations },
  { label: '控制台', to: ROUTES.console },
  { label: '登录', to: ROUTES.login },
] as const

const DOC_LINKS = [
  { label: '开发者文档', to: ROUTES.docs },
  { label: '快速开始', to: `${ROUTES.docs}?chapter=quickstart` },
  { label: '错误码与限流', to: `${ROUTES.docs}?chapter=errors` },
  { label: '工具集成配置', to: `${ROUTES.docs}?chapter=tools` },
] as const

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
            <Link
              to={`${ROUTES.login}?tab=register`}
              className={buttonVariants({ variant: 'primary', size: 'sm' })}
            >
              免费开始
            </Link>
          </div>
        </div>

        <nav
          aria-label="移动端导航"
          className="flex gap-1 overflow-x-auto border-t border-border px-4 py-2 md:hidden"
        >
          {SITE_NAV.map((item) => (
            <NavLink key={item.to} to={item.to} end={item.end} className={navLinkClass}>
              {item.label}
            </NavLink>
          ))}
        </nav>
      </header>

      <main id="main-content" className="flex-1">
        <Outlet />
      </main>

      <footer className="site-footer border-t border-border bg-surface">
        <div className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6">
          <div className="grid gap-8 lg:grid-cols-[1.1fr_2fr]">
            <div className="space-y-2">
              <Logo />
              <p className="max-w-xs text-xs leading-relaxed text-ink-muted">{BRAND_DESCRIPTION}</p>
            </div>

            <div className="grid grid-cols-2 gap-6 sm:grid-cols-4">
              <FooterGroup title="产品">
                {PRODUCT_LINKS.map((item) => (
                  <li key={item.label}>
                    <Link to={item.to} className="hover:text-ink">
                      {item.label}
                    </Link>
                  </li>
                ))}
              </FooterGroup>

              <FooterGroup title="文档">
                {DOC_LINKS.map((item) => (
                  <li key={item.label}>
                    <Link to={item.to} className="hover:text-ink">
                      {item.label}
                    </Link>
                  </li>
                ))}
              </FooterGroup>

              <FooterGroup title="协议">
                <li>用户协议</li>
                <li>隐私政策</li>
                <li>服务条款</li>
              </FooterGroup>

              <FooterGroup title="备案信息">
                <li className="text-xs leading-relaxed text-ink-muted">
                  证照与备案信息在正式上线前补充。
                </li>
              </FooterGroup>
            </div>
          </div>

          <div className="mt-8 flex flex-col gap-2 border-t border-border pt-4 sm:flex-row sm:items-center sm:justify-between">
            <Copyright />
            <p className="text-xs text-ink-muted">示例域名与 Key 均为虚拟值。</p>
          </div>
        </div>
      </footer>
    </div>
  )
}
