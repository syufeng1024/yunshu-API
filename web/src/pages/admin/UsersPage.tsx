// TODO(T20): implement PRD §6.3. Balance changes are append-only ledger entries;
// no direct update or delete of historical entries.
import { PageScaffold } from '@/components'

export default function AdminUsersPage() {
  return (
    <PageScaffold
      title="用户管理"
      description="账号、套餐、余额、Key 数量与状态；团队相关字段在 V1.0 不展示。"
      task="T20"
      sections={[
        '用户列表与搜索',
        '套餐与余额',
        'Key 数量与今日用量',
        '状态与角色',
        '余额调整（追加分录 + 理由留痕）',
        '用户详情抽屉（资料 / 余额 / Key / 用量 / 订单 / 操作记录）',
      ]}
    />
  )
}
