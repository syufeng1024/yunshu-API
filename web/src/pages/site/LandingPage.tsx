import { Link } from 'react-router-dom'
import {
  usePublicAnnouncements,
  usePublicEndpoints,
  usePublicModels,
  usePublicPlans,
} from '@/api/public'
import { errorMessage } from '@/api/client'
import { Badge, ErrorState, Icon, Skeleton, buttonVariants } from '@/components'
import type { IconName } from '@/lib/constants'
import { ROUTES } from '@/lib/constants'
import {
  AnnouncementRail,
  CodeBlock,
  EndpointBlock,
  ModelRecommendBlock,
  PlanLimitTable,
  PriceTable,
  SectionHeader,
} from '@/components/site'
import { CAPABILITIES } from '@/content/capabilities'
import { EXAMPLE_BASE_URL, TOOL_INTEGRATIONS } from '@/content/toolIntegrations'
import { formatInteger } from '@/lib/format'
import { selectListedModels, uniqueProtocols } from '@/lib/site'
import { usePageTitle } from '@/lib/usePageTitle'

const HERO_SNIPPET = `curl ${EXAMPLE_BASE_URL}/chat/completions \\
  -H "Authorization: Bearer sk-xxxxxxxx" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"<模型名>","messages":[{"role":"user","content":"ping"}]}'`

interface Stat {
  label: string
  value: string
}

function HeroStat({ stat }: { stat: Stat }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-ink-muted">{stat.label}</dt>
      <dd className="mt-1 text-xl font-semibold tabular-nums text-ink">{stat.value}</dd>
    </div>
  )
}

/**
 * A zero in the hero reads as "this platform has nothing" (D-24): there is no
 * t_endpoint table in V1.0, so /api/public/endpoints is honestly empty. An
 * unknown or absent count must render as "--", never as the literal 0 — the
 * same rule applies to the model and protocol counts when their data is empty.
 */
function formatStatValue(value: number): string {
  return value > 0 ? formatInteger(value) : '--'
}

function CapabilityCard({
  icon,
  index,
  title,
  description,
}: {
  icon: IconName
  index: number
  title: string
  description: string
}) {
  return (
    <article className="flex flex-col gap-3 rounded-xl border border-border bg-surface p-5 shadow-card transition-colors duration-fast hover:border-primary-ring">
      <div className="flex items-center justify-between gap-3">
        <span className="flex h-10 w-10 items-center justify-center rounded-lg bg-primary-soft text-primary">
          <Icon name={icon} className="h-5 w-5" />
        </span>
        <span className="font-mono text-xs text-ink-muted">{String(index).padStart(2, '0')}</span>
      </div>
      <h3 className="text-sm font-semibold text-ink">{title}</h3>
      <p className="text-sm leading-relaxed text-ink-secondary">{description}</p>
    </article>
  )
}

export default function LandingPage() {
  usePageTitle('首页')

  const modelsQuery = usePublicModels()
  const plansQuery = usePublicPlans()
  const announcementsQuery = usePublicAnnouncements()
  const endpointsQuery = usePublicEndpoints()

  const models = modelsQuery.data?.data ?? []
  const plans = plansQuery.data?.data ?? []
  const announcements = announcementsQuery.data?.data ?? []
  const endpoints = endpointsQuery.data?.data ?? []

  const listedModels = selectListedModels(models)
  const protocolCount = uniqueProtocols(models).length
  const statsLoading = modelsQuery.isLoading || endpointsQuery.isLoading
  const statsError = modelsQuery.isError || endpointsQuery.isError
  const statsErrorMessage = errorMessage(modelsQuery.error ?? endpointsQuery.error)

  const hasEndpoints = endpoints.length > 0
  // 数据条只放真实数字（D-24 修订）：V1.0 没有对外接入点，原来第三格填「筹备中」，
  // 夹在两个数字中间读起来像少填了一格，也等于主动告诉访客「还没上线」。因此
  // 没有接入点时整格不渲染，有接入点时照常显示线路条数。
  const stats: Stat[] = [
    { label: '已上架模型', value: formatStatValue(listedModels.length) },
    ...(hasEndpoints
      ? [{ label: '接入线路', value: formatStatValue(endpoints.length) }]
      : []),
    { label: '兼容协议', value: formatStatValue(protocolCount) },
  ]
  // Tailwind 只识别静态类名，这里用三元选完整的类名，不能拼 `grid-cols-${n}`。
  // 加载态骨架屏也用同一个数量，否则会先渲染 3 格再收回 2 格，首屏闪一下跳动。
  const statsColumnClass = stats.length > 2 ? 'grid-cols-3' : 'grid-cols-2'
  const statsErrorSpanClass = stats.length > 2 ? 'col-span-3' : 'col-span-2'

  return (
    <div>
      <section data-section="hero" className="relative overflow-hidden border-b border-border bg-surface">
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_right,var(--primary-soft),transparent_58%)]"
        />
        {/* 首屏节奏：header 底边到徽标之间只留一档呼吸（py-12 / lg:py-16）。
            原来 lg 下上下各 96px，把约 500px 的内容撑成 646px。注意收益在
            1366×768 这一档而不是 1440×900：1440×900 下下一区块标题本来就在
            首屏内（顶部 791px < 900），而 1366×768 里 791 > 768，标题落在
            折叠线以下；收紧后标题顶部 707px，余量 61px。这条约束由
            e2e/responsive.spec.ts 的「首屏：1366×768」用例守住。 */}
        <div className="relative mx-auto grid w-full max-w-6xl gap-10 px-4 py-12 sm:px-6 lg:grid-cols-[1.1fr_0.9fr] lg:items-center lg:py-16">
          {/* G19: 两个列子项都要 min-w-0。grid 子项的自动最小尺寸默认是
              min-content，右侧代码块的 <pre> 是 510px 的不可断行内容，会把整列
              撑到 546px；外层 hero section 的 overflow-hidden（裁径向渐变用）
              随后把溢出部分静默裁掉，表现成「h1 和正文右侧被切」。 */}
          <div className="min-w-0 max-w-2xl">
            <Badge variant="primary" dot>
              V1.0 · 境内模型统一接入
            </Badge>
            <h1 className="mt-4 text-balance text-4xl font-semibold leading-tight tracking-tight text-ink sm:text-5xl">
              一个 Base URL，接入多家境内模型
            </h1>
            <p className="mt-3 max-w-xl text-base leading-relaxed text-ink-secondary">
              保持客户端现有的 OpenAI 或 Anthropic 调用方式，只替换地址、Key 与模型名。计费、限流与日志在同一个控制台完成。
            </p>

            <ul className="mt-4 flex flex-wrap gap-x-5 gap-y-2 text-sm text-ink-secondary">
              {['主流模型聚合', '换地址即用', '积分按量抵扣'].map((item) => (
                <li key={item} className="flex items-center gap-1.5">
                  <Icon name="check" className="h-4 w-4 text-success" />
                  {item}
                </li>
              ))}
            </ul>

            <div className="mt-6 flex flex-wrap items-center gap-3">
              <Link
                to={`${ROUTES.login}?tab=register`}
                className={buttonVariants({ variant: 'primary', size: 'lg' })}
              >
                免费开始
              </Link>
              <Link
                to={ROUTES.docs}
                className={buttonVariants({ variant: 'secondary', size: 'lg' })}
              >
                查看文档
              </Link>
            </div>
            <p className="mt-3 text-xs text-ink-muted">
              注册后可主动领取体验积分，具体额度以控制台展示为准。
            </p>

            <dl
              data-testid="hero-stats"
              className={`mt-7 grid gap-4 border-t border-border pt-6 ${statsColumnClass}`}
            >
              {statsError ? (
                <div className={statsErrorSpanClass}>
                  <ErrorState
                    compact
                    message={statsErrorMessage}
                    onRetry={() => {
                      if (modelsQuery.isError) void modelsQuery.refetch()
                      if (endpointsQuery.isError) void endpointsQuery.refetch()
                    }}
                  />
                </div>
              ) : statsLoading ? (
                Array.from({ length: stats.length }, (_, index) => (
                  <div key={index} className="space-y-2">
                    <Skeleton className="h-3 w-16" />
                    <Skeleton className="h-6 w-12" />
                  </div>
                ))
              ) : (
                stats.map((stat) => <HeroStat key={stat.label} stat={stat} />)
              )}
            </dl>
          </div>

          <div className="min-w-0 rounded-2xl border border-border bg-canvas p-4 shadow-pop sm:p-5">
            <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                <Badge variant="neutral">OpenAI 兼容</Badge>
                <Badge variant="neutral">Anthropic</Badge>
              </div>
              <span className="font-mono text-xs text-ink-muted">POST /chat/completions</span>
            </div>
            <CodeBlock code={HERO_SNIPPET} label="curl" />

            <div className="mt-4 rounded-xl border border-border bg-surface p-4">
              <p className="text-xs text-ink-muted">需要填写的地址</p>
              <p className="mt-1 break-all font-mono text-sm text-ink">{EXAMPLE_BASE_URL}</p>
              <p className="mt-1 text-xs text-ink-secondary">
                OpenAI 兼容客户端保留 /v1；Anthropic 客户端只填到域名层。
              </p>
            </div>
          </div>
        </div>
      </section>

      <section data-section="capabilities" className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
        <SectionHeader
          eyebrow="WHY YUNSHU"
          title="从接入到计费，一条链路可核对"
          description="六个能力点分别对应网关、计费与可观测性中的具体机制，不靠一句笼统的「稳定」。"
          action={
            <Link to={ROUTES.docs} className={buttonVariants({ variant: 'ghost', size: 'sm' })}>
              查看接入文档
            </Link>
          }
        />
        <div className="mt-8 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {CAPABILITIES.map((capability, index) => (
            <CapabilityCard
              key={capability.title}
              icon={capability.icon}
              index={index + 1}
              title={capability.title}
              description={capability.description}
            />
          ))}
        </div>
      </section>

      <ModelRecommendBlock
        models={models}
        loading={modelsQuery.isLoading}
        error={modelsQuery.isError ? errorMessage(modelsQuery.error) : null}
        onRetry={() => {
          void modelsQuery.refetch()
        }}
      />

      <section data-section="models" className="border-y border-border bg-surface">
        <div className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
          <SectionHeader
            eyebrow="MODELS"
            title="模型价格表"
            description="按文本与图像分组展示已上架模型，单价为标准分组公开价。"
            action={
              <Link to={ROUTES.pricing} className={buttonVariants({ variant: 'ghost', size: 'sm' })}>
                查看套餐与计费规则
              </Link>
            }
          />
          <div className="mt-8">
            <PriceTable
              models={models}
              loading={modelsQuery.isLoading}
              error={modelsQuery.isError ? errorMessage(modelsQuery.error) : null}
              onRetry={() => {
                void modelsQuery.refetch()
              }}
            />
          </div>
        </div>
      </section>

      <section data-section="plans" className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
        <SectionHeader
          eyebrow="PLANS"
          title="套餐限流规格"
          description="四档套餐分别配置 RPM、TPM、并发与账户日额度；默认档不单独售卖。"
          action={
            <Link to={ROUTES.pricing} className={buttonVariants({ variant: 'ghost', size: 'sm' })}>
              查看定价详情
            </Link>
          }
        />
        <div className="mt-8">
          <PlanLimitTable
            plans={plans}
            loading={plansQuery.isLoading}
            error={plansQuery.isError ? errorMessage(plansQuery.error) : null}
            onRetry={() => {
              void plansQuery.refetch()
            }}
          />
        </div>
      </section>

      {/*
        没有公告时整块不渲染：首页是营销页，一块 400px 高的虚线「暂无」空壳
        夹在价格表与工具集成之间，会把页面读成「还没做完」。等运营真的发布
        公告，这一块自己长出来；接口失败同样不渲染（错误由章节内的重试入口
        承担，不在营销页堆一块红色空态）。
      */}
      {announcements.length > 0 ? (
        <section data-section="announcements" className="border-y border-border bg-surface">
          <div className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
            <SectionHeader
              eyebrow="NOTICE"
              title="调价公告"
              description="价格或限流规格调整前在此公示，并标明生效时间。"
            />
            <div className="mt-8">
              <AnnouncementRail
                announcements={announcements}
                loading={announcementsQuery.isLoading}
                error={announcementsQuery.isError ? errorMessage(announcementsQuery.error) : null}
                onRetry={() => {
                  void announcementsQuery.refetch()
                }}
              />
            </div>
          </div>
        </section>
      ) : null}

      {/*
        接入点同理：V1.0 没有对外线路，空态是一块 422px 的虚线框。Base URL
        这件事在下方「快速接入三步」与文档页都有交代，营销页不需要为它留一个
        空壳占位。
      */}
      {hasEndpoints ? (
        <section data-section="endpoints" className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
          <SectionHeader
            eyebrow="ENDPOINTS"
            title="接入点与线路"
            description="Base URL 是唯一需要替换的地址；主接入点与灾备线路由后台维护。"
          />
          <div className="mt-8">
            <EndpointBlock
              endpoints={endpoints}
              loading={endpointsQuery.isLoading}
              error={endpointsQuery.isError ? errorMessage(endpointsQuery.error) : null}
              onRetry={() => {
                void endpointsQuery.refetch()
              }}
            />
          </div>
        </section>
      ) : null}

      <section data-section="tools" className="border-y border-border bg-surface">
        <div className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
          <SectionHeader
            eyebrow="INTEGRATIONS"
            title="工具集成入口"
            description="你现在用的工具，把地址换一下就能用。"
            action={
              <Link
                to={ROUTES.integrations}
                className={buttonVariants({ variant: 'primary', size: 'sm' })}
              >
                查看配置步骤
              </Link>
            }
          />
          <ul className="mt-8 grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
            {TOOL_INTEGRATIONS.map((tool) => (
              <li key={tool.key}>
                <Link
                  to={ROUTES.integrations}
                  className="flex h-full flex-col justify-between gap-3 rounded-xl border border-border bg-canvas p-4 transition-colors duration-fast hover:border-primary-ring"
                >
                  <span className="text-sm font-semibold text-ink">{tool.name}</span>
                  <span className="flex items-center justify-between gap-2 text-xs text-ink-muted">
                    <span>{tool.kind}</span>
                    <Icon name="chevron-right" className="h-3.5 w-3.5" />
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        </div>
      </section>

      <section data-section="quickstart" className="mx-auto w-full max-w-6xl px-4 py-14 sm:px-6">
        <SectionHeader
          eyebrow="QUICK START"
          title="快速接入三步"
          description="注册、创建 Key、替换地址。第一次请求通常只差这三步。"
        />
        <ol className="mt-8 grid gap-4 md:grid-cols-3">
          {[
            {
              title: '注册并登录',
              detail: '使用邮箱或手机号注册，登录后进入控制台。',
            },
            {
              title: '创建 Key',
              detail: '在密钥管理页创建密钥，并按需勾选授权模型。',
            },
            {
              title: '替换地址',
              detail: '把 Base URL 填进现有工具，模型名从 /v1/models 返回列表中选择。',
            },
          ].map((step, index) => (
            <li key={step.title} className="rounded-xl border border-border bg-surface p-5 shadow-card">
              <p className="font-mono text-xs text-primary">{String(index + 1).padStart(2, '0')}</p>
              <h3 className="mt-2 text-sm font-semibold text-ink">{step.title}</h3>
              <p className="mt-1 text-sm leading-relaxed text-ink-secondary">{step.detail}</p>
            </li>
          ))}
        </ol>
      </section>

      <section data-section="cta" className="mx-auto w-full max-w-6xl px-4 pb-16 sm:px-6">
        <div className="overflow-hidden rounded-2xl border border-primary-ring bg-primary-soft px-6 py-10 text-center sm:px-10">
          <h2 className="text-2xl font-semibold tracking-tight text-ink sm:text-3xl">
            从一次成功的请求开始
          </h2>
          <p className="mx-auto mt-3 max-w-xl text-sm leading-relaxed text-ink-secondary">
            注册后创建 Key，把 Base URL 填进现有工具即可。体验积分需在控制台主动领取。
          </p>
          <div className="mt-6 flex flex-wrap items-center justify-center gap-3">
            <Link
              to={`${ROUTES.login}?tab=register`}
              className={buttonVariants({ variant: 'primary', size: 'lg' })}
            >
              免费开始
            </Link>
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
