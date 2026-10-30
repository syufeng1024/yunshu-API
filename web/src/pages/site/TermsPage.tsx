import { TERMS_DOCUMENT } from '@/content/legal'
import { ROUTES } from '@/lib/constants'
import { LegalDoc } from './legal/LegalDoc'

/**
 * /terms —— 用户协议占位页。
 *
 * 章节骨架与锚点（含 #service-terms）来自 `src/content/legal.ts`：条款正文由
 * 法务提供前不写任何实质内容，页面只标注「正式上线前补充 · 由法务定稿」。
 */
export default function TermsPage() {
  return (
    <LegalDoc
      doc={TERMS_DOCUMENT}
      siblings={[{ label: '隐私政策', to: ROUTES.privacy }]}
    />
  )
}
