// TODO(T19): implement PRD §5.3. Route param :id requires an ownership check;
// non-owned keys return 403 or "not found" and render an error state.
import { PageScaffold } from '@/components'

export default function KeyDetailPage() {
  return (
    <PageScaffold
      title="密钥详情"
      description="基础信息、实时指标、模型权限、IP 白名单与按日用量。"
      task="T19"
      sections={[
        '基础信息（名称 / 前缀 / 状态 / 创建时间 / 最近使用）',
        '分组与有效期（只读）',
        '实时 RPM / TPM / 并发与迷你趋势',
        '账户日额度进度与触顶预测',
        '模型权限勾选',
        'IP 白名单增删',
        '按日 / 按模型用量明细',
        '编辑、停用、删除与复制前缀',
      ]}
    />
  )
}
