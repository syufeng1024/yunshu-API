// TODO(T19): implement PRD §5.1. Every metric, chart and ranking comes from the
// aggregation API; a failed request must render an error state, never zeros.
import { PageScaffold } from '@/components'

export default function ConsoleOverviewPage() {
  return (
    <PageScaffold
      title="控制台 · 概览"
      description="账户今日消耗、余额、请求数与成功率的单一入口。"
      task="T19"
      sections={[
        '4 个核心指标（消耗 / 余额 / 请求数 / 成功率）',
        '账户日额度进度与触顶预测',
        '24 小时双线趋势图',
        '失败原因分布',
        '模型用量排行',
        '渠道用量排行',
        '最近调用日志',
        '公告条',
      ]}
    />
  )
}
