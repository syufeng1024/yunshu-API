// TODO(T19): implement PRD §5.2. The list supports name search, status filter,
// read-only group display and the create wizard; full keys are shown once.
import { PageScaffold } from '@/components'

export default function KeysPage() {
  return (
    <PageScaffold
      title="密钥管理"
      description="单用户多 Key：名称、前缀、状态、分组、有效期与今日用量。"
      task="T19"
      sections={[
        '列表（名称 / 前缀 / 状态 / 分组 / 有效期 / 今日用量）',
        '按名称搜索与状态筛选',
        '新建密钥向导（不出现分组选择步骤）',
        '创建成功后一次性展示完整 Key',
        '停用与删除二次确认',
        '活跃 Key 数量上限提示',
      ]}
    />
  )
}
