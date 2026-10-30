import { PRIVACY_DOCUMENT } from '@/content/legal'
import { ROUTES } from '@/lib/constants'
import { LegalDoc } from './legal/LegalDoc'

/**
 * /privacy —— 隐私政策占位页。
 *
 * 与 /terms 同构：章节骨架来自 `src/content/legal.ts`，正文待法务定稿，页面只
 * 标注「正式上线前补充 · 由法务定稿」。
 */
export default function PrivacyPage() {
  return (
    <LegalDoc
      doc={PRIVACY_DOCUMENT}
      siblings={[{ label: '用户协议', to: ROUTES.terms }]}
    />
  )
}
