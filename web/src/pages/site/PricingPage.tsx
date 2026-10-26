// TODO(T18): implement PRD §4.2. Plan prices, quotas and bonuses come from the
// plan configuration API; no value may be embedded in this bundle.
import { PageScaffold } from '@/components'

export default function PricingPage() {
  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6">
      <PageScaffold
        title="定价"
        description="四档套餐与限流规格对比，数值全部取自后台套餐配置。"
        task="T18"
        sections={[
          '套餐卡片区（体验版 / 标准版 / 专业版 / 企业版）',
          '限流额度对比表',
          '计费规则 FAQ',
          '底部 CTA',
        ]}
      />
    </div>
  )
}
