// TODO(T19): implement PRD §5.9. Ticket SLA is first response <= 4h and reply
// <= 48h; the list must show the server-computed state, not a local guess.
import { PageScaffold } from '@/components'

export default function ConsoleTicketsPage() {
  return (
    <PageScaffold
      title="工单中心"
      description="提交问题、查看处理进度与 SLA 状态。"
      task="T19"
      sections={[
        '工单列表与状态',
        '新建工单（分类 / 描述 / 附件）',
        '会话与回复',
        'SLA 首次响应与答复时限',
        '关闭与重新打开',
      ]}
    />
  )
}
