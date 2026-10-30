/**
 * Single copy source for "material still missing before go-live".
 *
 * Scope is deliberately narrow: this string only marks **资质/备案/法务类**
 * material that the operator has not supplied yet (ICP 备案号、增值电信业务
 * 许可证、软件著作权、法律条款定稿). Empty-state copy such as「暂无公告」or
 *「暂无调价公告」describes "the API returned no rows", which is a different
 * fact and must **not** be rewritten to this string — mixing the two would
 * tell the user a licence is pending when nothing is pending at all.
 */

/** Marks a place where licence / filing / legal material is still missing. */
export const PENDING_BEFORE_LAUNCH = '正式上线前补充'

/** Badge wording for a page whose全文 is still a placeholder. */
export const PENDING_LEGAL_BADGE = `${PENDING_BEFORE_LAUNCH} · 由法务定稿`

/** Body of every legal section while the text is not finalised. */
export const PENDING_SECTION_BODY = '本节内容待定稿。'
