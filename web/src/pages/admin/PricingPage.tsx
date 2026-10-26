// TODO(T20): implement PRD §6.4. Model names are read-only echoes from the
// model master data page; only price fields are edited here.
import { PageScaffold } from '@/components'

export default function AdminPricingPage() {
  return (
    <PageScaffold
      title="倍率与定价"
      description="模型单价、缓存倍率、长上下文阶梯、分组倍率与套餐配置。"
      task="T20"
      sections={[
        '模型定价表（只读回显模型名）',
        '缓存命中 / 写入倍率',
        '长上下文阈值与阶梯单价',
        '分组倍率配置（修改须填理由）',
        '上游成本单价与毛利视图',
        '套餐配置与毛利地板校验',
        '变更记录',
      ]}
    />
  )
}
