// TODO(T19): implement PRD §5.7. Log rows carry request_id, model, status,
// latency and cost; filtering is server-side.
import { PageScaffold } from '@/components'

export default function LogsPage() {
  return (
    <PageScaffold
      title="请求日志"
      description="按请求维度追溯耗时、状态、扣费与重试链路。"
      task="T19"
      sections={[
        '日志列表（时间 / 模型 / 状态 / 耗时 / 扣费）',
        '按时间、模型、Key 与状态筛选',
        '请求详情与重试链路',
        '失败请求不计费标注',
        '分页与排序',
      ]}
    />
  )
}
