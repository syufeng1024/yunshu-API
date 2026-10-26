// TODO(T20): implement PRD §6.7. Model name, alias and listing state have this
// page as their only write point; pricing echoes them read-only.
import { PageScaffold } from '@/components'

export default function AdminModelsPage() {
  return (
    <PageScaffold
      title="模型主数据"
      description="模型名称、别名、协议、能力标签、上下架状态与推荐权重。"
      task="T20"
      sections={[
        '模型列表与搜索',
        '模型名称与别名（唯一写入口）',
        '协议与能力标签',
        '上下架状态（已上架 / 仅目录）',
        '推荐权重与排序',
        '渠道映射入口',
      ]}
    />
  )
}
