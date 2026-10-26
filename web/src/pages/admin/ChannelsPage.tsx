// TODO(T20): implement PRD §6.2. Source region "overseas" is rejected on save;
// upstream keys are encrypted at rest and masked in the UI.
import { PageScaffold } from '@/components'

export default function AdminChannelsPage() {
  return (
    <PageScaffold
      title="渠道管理"
      description="上游渠道、来源地、协议、权重、分组范围与专属用户。"
      task="T20"
      sections={[
        '渠道列表（名称 / 来源地 / 协议 / 权重 / 状态 / 成功率）',
        '新增与编辑渠道',
        '来源地选「境外」拒绝保存',
        '允许的分组与专属用户',
        '启用 / 停用 / 删除二次确认',
        '测试连接',
      ]}
    />
  )
}
