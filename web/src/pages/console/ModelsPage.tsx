// TODO(T19): implement PRD §5.4. Only listed models are shown; prices arrive
// as micro-credits per 1k tokens and are rendered through lib/format.ts.
import { PageScaffold } from '@/components'

export default function ConsoleModelsPage() {
  return (
    <PageScaffold
      title="模型广场"
      description="仅展示已上架模型；单价、上下文与能力标签均来自模型主数据接口。"
      task="T19"
      sections={[
        '模型列表（名称 / 类型 / 上下文 / 单价 / 协议 / 能力）',
        '按类型与协议筛选',
        '按名称或别名搜索',
        '模型详情、调用示例与计费示例',
        '缓存计价关闭时不展示缓存价',
        '推荐权重排序',
      ]}
    />
  )
}
