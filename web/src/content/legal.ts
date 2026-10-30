import { PENDING_SECTION_BODY } from '@/lib/placeholders'

/**
 * Structure-only source for /terms and /privacy.
 *
 * 需求方口径：法律条款**尚未提供**，所以这里只有章节骨架——每节标题是真实的
 * 结构信息（法务按此目录定稿），正文统一为占位文案。**不要**在此文件中补写
 * 任何看起来像条款的实质内容（责任限制、退款比例、保留期限等），否则用户会
 * 把它当成本平台的真实承诺。定稿时整篇替换本文件的 `sections` 即可，页面组件
 * 不用改。
 */

export type LegalDocumentId = 'terms' | 'privacy'

export interface LegalSection {
  /** 页面内锚点 id；`service-terms` 被页脚「服务条款」直接引用，勿改。 */
  id: string
  title: string
  /** 定稿前的占位正文；定稿后换成真实条款段落。 */
  body: string
}

export interface LegalDocument {
  id: LegalDocumentId
  /** 页面 h1 与 <title> 的页面名。 */
  title: string
  /** h1 下方的一句话说明（属于结构性说明，不是条款）。 */
  summary: string
  sections: readonly LegalSection[]
}

/** 页脚「服务条款」锚点目标；同时被 e2e 断言引用。 */
export const SERVICE_TERMS_SECTION_ID = 'service-terms'

/**
 * 全站统一下方提示：这一页为什么是空的、以及当前内容不构成承诺。
 */
export const LEGAL_PLACEHOLDER_NOTICE =
  '本页为占位骨架，条款正文由法务定稿后在正式上线前补充；当前内容不构成任何法律承诺。'

function section(id: string, title: string): LegalSection {
  return { id, title, body: PENDING_SECTION_BODY }
}

export const TERMS_DOCUMENT: LegalDocument = {
  id: 'terms',
  title: '用户协议',
  summary:
    '本协议约定你与平台之间关于账号、计费与使用规范的约定；各章节标题即定稿目录，正文待法务出具。',
  sections: [
    section('scope', '适用范围'),
    section('account', '账号与实名'),
    section('billing', '计费与退款'),
    section('usage', '使用规范'),
    section('liability', '责任限制'),
    section('changes', '变更与通知'),
    // 页脚「服务条款」指向本节（/terms#service-terms），id 必须保持稳定。
    section(SERVICE_TERMS_SECTION_ID, '服务条款'),
  ],
}

export const PRIVACY_DOCUMENT: LegalDocument = {
  id: 'privacy',
  title: '隐私政策',
  summary:
    '本政策说明平台收集哪些信息、为什么收集、保留多久，以及你可以如何行使权利；正文待法务出具。',
  sections: [
    section('collected', '收集的信息'),
    section('purpose', '使用目的'),
    section('retention', '存储与保留'),
    section('sharing', '第三方共享'),
    section('rights', '用户权利'),
    section('contact', '联系方式'),
  ],
}
