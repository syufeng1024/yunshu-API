// TODO(T20): implement PRD §6.6. Quota-increase requests and other review
// queues share one shell; decisions require an operator identity and reason.
import { PageScaffold } from '@/components'

export default function AdminReviewsPage() {
  return (
    <PageScaffold
      title="审核中心"
      description="提额申请与其他审核队列的统一入口。"
      task="T20"
      sections={[
        '提额申请 Tab',
        '待审核列表与筛选',
        '申请详情与证据',
        '通过 / 驳回（填写理由）',
        '操作留痕',
      ]}
    />
  )
}
