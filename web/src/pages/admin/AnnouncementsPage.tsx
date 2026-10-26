// TODO(T20): implement PRD §6.5. Price or quota changes must be announced on
// the landing page before taking effect.
import { PageScaffold } from '@/components'

export default function AdminAnnouncementsPage() {
  return (
    <PageScaffold
      title="公告与风控"
      description="公告发布、调价缓冲期与风控规则。"
      task="T20"
      sections={[
        '公告列表与发布',
        '首页滚动公告位',
        '调价生效时间与缓冲期',
        '风控规则与命中记录',
        '误杀复核入口',
      ]}
    />
  )
}
