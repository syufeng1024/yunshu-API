// TODO(T20): implement PRD §6.1. Success rate uses the internal formula and
// must be labelled separately from the customer-facing figure.
import { PageScaffold } from '@/components'

export default function AdminOverviewPage() {
  return (
    <PageScaffold
      title="运营概览"
      description="平台调用、消耗、活跃用户与渠道健康度的总览。"
      task="T20"
      sections={[
        '核心指标（调用量 / 消耗 / 活跃用户 / 渠道健康度）',
        '近 30 日收入与消耗趋势',
        '渠道健康度排行',
        '用户消耗排行',
        '模型消耗排行',
        '告警区（渠道异常 / 余额不足 / 限流频发 / SLA / 透支冻结）',
      ]}
    />
  )
}
