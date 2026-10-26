// TODO(T18): implement PRD §4.1 sections against the public pricing/plan APIs.
import { PageScaffold } from '@/components'

export default function LandingPage() {
  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-10 sm:px-6">
      <PageScaffold
        title="首页"
        description="对外落地页：品牌、能力、价格与接入点全部由接口驱动。"
        task="T18"
        sections={[
          'Hero 区（主标题、副标题、双 CTA）',
          '核心能力区（4～6 张能力卡）',
          '模型价格表（数据来自定价接口）',
          '套餐限流表（数据来自套餐配置）',
          '调价公告位',
          '接入点与线路',
          '工具集成入口',
          '底部 CTA',
        ]}
      />
    </div>
  )
}
