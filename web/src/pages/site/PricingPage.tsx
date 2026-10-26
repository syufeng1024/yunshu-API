import { Link } from 'react-router-dom'
import { usePublicPlans } from '@/api/public'
import { errorMessage } from '@/api/client'
import { Badge, Icon, buttonVariants } from '@/components'
import { PlanCards, PlanLimitTable, SectionHeader } from '@/components/site'
import { BILLING_FAQ } from '@/content/billingFaq'
import { ROUTES } from '@/lib/constants'
import { usePageTitle } from '@/lib/usePageTitle'
import { useAuthStore } from '@/stores/auth'

export default function PricingPage() {
  usePageTitle('定价')

  const plansQuery = usePublicPlans()
  const plans = plansQuery.data?.data ?? []
  const authenticated = useAuthStore((state) => state.status) === 'authenticated'
  const plansError = plansQuery.isError ? errorMessage(plansQuery.error) : null
  const retry = () => {
    void plansQuery.refetch()
  }

  return (
    <div>
      <section className="border-b border-border bg-surface">
        <div className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
          <Badge variant="primary" dot>
            一次性购买 · 不自动续费
          </Badge>
          <h1 className="mt-4 text-3xl font-semibold tracking-tight text-ink sm:text-4xl">
            按调用规模选择套餐
          </h1>
          <p className="mt-3 max-w-2xl text-sm leading-relaxed text-ink-secondary">
            四档套餐对应不同的 RPM、TPM、并发与账户日额度。单价按模型计费，套餐只决定限流规格与赠送积分，不改变模型的公开标准单价。
          </p>
        </div>
      </section>

      <section id="plan-cards" className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
        <SectionHeader
          eyebrow="PLANS"
          title="套餐卡片"
          description="赠送积分、限流规格、可调用模型范围、发票与 SLA 均取自套餐配置。"
        />
        <div className="mt-8">
          <PlanCards
            plans={plans}
            loading={plansQuery.isLoading}
            error={plansError}
            onRetry={retry}
            authenticated={authenticated}
          />
        </div>
        <p className="mt-4 text-xs text-ink-muted">
          未登录时点击套餐会进入注册并携带套餐标识；已登录时进入充值中心并预选该档。
        </p>
      </section>

      <section className="border-y border-border bg-surface">
        <div className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
          <SectionHeader
            eyebrow="COMPARE"
            title="四档限流规格对比"
            description="六个字段放在同一张表内对比，避免在长段落里找差异。"
          />
          <div className="mt-8">
            <PlanLimitTable
              plans={plans}
              loading={plansQuery.isLoading}
              error={plansError}
              onRetry={retry}
            />
          </div>
        </div>
      </section>

      <section className="mx-auto w-full max-w-4xl px-4 py-14 sm:px-6">
        <SectionHeader
          eyebrow="BILLING"
          title="计费规则 FAQ"
          description="积分计算、失败计费、充值、开票与提额口径。"
        />
        <div className="mt-8 divide-y divide-border rounded-xl border border-border bg-surface">
          {BILLING_FAQ.map((item) => (
            <details key={item.id} className="group px-5 py-4">
              <summary className="flex cursor-pointer list-none items-center justify-between gap-3 text-sm font-medium text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary">
                {item.question}
                <Icon
                  name="chevron-down"
                  className="h-4 w-4 shrink-0 text-ink-muted transition-transform duration-fast group-open:rotate-180"
                />
              </summary>
              <p className="mt-3 text-sm leading-relaxed text-ink-secondary">{item.answer}</p>
            </details>
          ))}
        </div>
      </section>

      <section className="mx-auto w-full max-w-6xl px-4 pb-16 sm:px-6">
        <div className="rounded-2xl border border-border bg-surface px-6 py-10 text-center shadow-card sm:px-10">
          <h2 className="text-2xl font-semibold tracking-tight text-ink">选择套餐或咨询企业采购</h2>
          <p className="mx-auto mt-3 max-w-xl text-sm leading-relaxed text-ink-secondary">
            套餐为一次性购买。购买前可先在文档页完成一次最小请求，确认模型与协议匹配。
          </p>
          <div className="mt-6 flex flex-wrap items-center justify-center gap-3">
            <a
              href="#plan-cards"
              className={buttonVariants({ variant: 'primary', size: 'lg' })}
            >
              立即购买
            </a>
            <Link
              to={`${ROUTES.login}?tab=register&intent=sales`}
              className={buttonVariants({ variant: 'secondary', size: 'lg' })}
            >
              联系销售
            </Link>
          </div>
        </div>
      </section>
    </div>
  )
}
