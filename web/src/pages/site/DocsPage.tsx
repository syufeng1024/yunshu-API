// TODO(T18): implement PRD §4.3 with the nine documentation chapters. Error
// codes must be rendered from the same source as the API contract (appendix E),
// never duplicated in page copy.
import { PageScaffold } from '@/components'

export default function DocsPage() {
  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6">
      <PageScaffold
        title="开发者文档"
        description="左侧章节导航 + 右侧正文；代码示例中的域名、Key 与请求 ID 均为虚拟值。"
        task="T18"
        sections={[
          '快速开始（curl / Python / Node）',
          '鉴权与请求头',
          '协议兼容（OpenAI 兼容 / Anthropic）',
          '流式响应（SSE）',
          '图像生成（仅在上架图像模型时展示）',
          '错误码与限流',
          '积分与账单',
          '新增渠道与路径',
          '工具集成',
        ]}
      />
    </div>
  )
}
