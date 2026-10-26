// TODO(T19): implement PRD §5.5. Aggregation by model/key/channel, monthly
// bills and CSV export with the active filters.
import { PageScaffold } from '@/components'

export default function UsagePage() {
  return (
    <PageScaffold
      title="用量与账单"
      description="按时间维度切换聚合口径，金额换算统一走 lib/format.ts。"
      task="T19"
      sections={[
        '时间维度（今日 / 近 7 日 / 本月 / 自定义）',
        '按模型聚合',
        '按 Key 聚合',
        '按渠道聚合',
        '月度账单与发票入口',
        'CSV 导出（携带当前筛选条件）',
      ]}
    />
  )
}
