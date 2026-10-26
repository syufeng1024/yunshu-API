// TODO(T20): implement PRD §6.7. Admin ticket handling shares the SLA clock
// with the console ticket centre.
import { PageScaffold } from '@/components'

export default function AdminTicketsPage() {
  return (
    <PageScaffold
      title="工单处理"
      description="用户工单的分派、回复与 SLA 计时。"
      task="T20"
      sections={[
        '工单队列与优先级',
        '按状态 / 分类 / 责任人筛选',
        '会话与内部备注',
        'SLA 超时告警',
        '分派与关闭',
      ]}
    />
  )
}
