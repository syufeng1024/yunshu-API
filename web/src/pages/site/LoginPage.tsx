// TODO(T18): implement login/register forms, verification code, agreement
// checkbox and post-login routing by the server-returned role.
import { Badge, EmptyState, PageHeader, Tabs } from '@/components'
import { usePageTitle } from '@/lib/usePageTitle'

const PANEL_CLASS = 'rounded-lg border border-dashed border-border-strong bg-surface-muted p-4'

export default function LoginPage() {
  usePageTitle('登录 / 注册')

  return (
    <div className="mx-auto w-full max-w-5xl px-4 py-10 sm:px-6">
      <PageHeader
        title="登录 / 注册"
        description="页内双 Tab 切换，登录后按账号真实角色跳转。"
        actions={<Badge variant="primary">页面骨架</Badge>}
      />

      <div className="mt-6 grid gap-6 lg:grid-cols-2">
        <section className="rounded-xl border border-border bg-surface p-6 shadow-card">
          <Tabs
            ariaLabel="登录或注册"
            items={[
              {
                id: 'login',
                label: '登录',
                content: (
                  <div className={PANEL_CLASS}>
                    <EmptyState
                      compact
                      title="登录表单待接入"
                      description="账号 + 密码 / 验证码、忘记密码与第三方登录在 T18 实现。"
                    />
                  </div>
                ),
              },
              {
                id: 'register',
                label: '注册',
                content: (
                  <div className={PANEL_CLASS}>
                    <EmptyState
                      compact
                      title="注册表单待接入"
                      description="邮箱 / 手机号、验证码、密码与协议勾选在 T18 实现；注册后按服务端规则开通体验版。"
                    />
                  </div>
                ),
              },
            ]}
          />
        </section>

        <aside className="h-fit space-y-4 rounded-xl border border-border bg-surface-muted p-6">
          <h2 className="text-sm font-semibold text-ink">登录后跳转</h2>
          <ul className="list-disc space-y-1 pl-5 text-sm text-ink-secondary">
            <li>个人账号进入控制台。</li>
            <li>运营、财务与超级管理员进入运营后台。</li>
          </ul>
          <p className="text-xs text-ink-muted">
            TODO(T18)：登录接口返回角色，前端不提供任何角色切换入口。
          </p>
        </aside>
      </div>
    </div>
  )
}
