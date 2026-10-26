// TODO(T18): implement PRD §4.5. Deep-link parameters are assembled only at the
// click moment and discarded; keys must never enter the URL, DOM, storage or
// analytics (F4-2).
import { PageScaffold } from '@/components'

export default function IntegrationsPage() {
  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6">
      <PageScaffold
        title="工具集成"
        description="面向已经选定客户端的用户，说明 Base URL 填到哪一层、模型名怎么写。"
        task="T18"
        sections={[
          '工具卡片列表（Cursor / Claude Code / Cherry Studio / NextChat / OpenAI SDK）',
          '工具详情与分步配置',
          '一键导入（Key 仅点击瞬间拼接，跳转后丢弃）',
          '常见报错与排查',
          'OpenAI SDK 通用兜底',
        ]}
      />
    </div>
  )
}
