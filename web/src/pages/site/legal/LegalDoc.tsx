import { useEffect } from 'react'
import { Link, useLocation } from 'react-router-dom'
import { Badge } from '@/components'
import { LEGAL_PLACEHOLDER_NOTICE } from '@/content/legal'
import type { LegalDocument } from '@/content/legal'
import { ROUTES } from '@/lib/constants'
import { PENDING_LEGAL_BADGE } from '@/lib/placeholders'
import { usePageTitle } from '@/lib/usePageTitle'

/**
 * Shared renderer for the two legal placeholder pages (/terms, /privacy).
 *
 * 只渲染 `src/content/legal.ts` 给出的章节骨架——本组件不承载任何条款文字，
 * 定稿时替换 content 即可，页面结构不用动。
 */

interface LegalDocProps {
  doc: LegalDocument
  /** 另一份文档的链接（协议页互指），避免用户在两页之间来回找。 */
  siblings: readonly { label: string; to: string }[]
}

export function LegalDoc({ doc, siblings }: LegalDocProps) {
  usePageTitle(doc.title)
  const location = useLocation()

  // 页脚「服务条款」是 `/terms#service-terms`（react-router 的 <Link>）。
  // <ScrollRestoration /> 只在自己的 layout effect 里按 hash 滚一次，而本页是
  // lazy 路由——那一刻章节还没挂载，getElementById 返回 null，于是回落成
  // scrollTo(0, 0)。这里在章节渲染完成后再滚一次，锚点才真的落到那一节。
  useEffect(() => {
    const raw = location.hash.startsWith('#') ? location.hash.slice(1) : ''
    if (!raw) return
    const target = window.document.getElementById(decodeURIComponent(raw))
    if (target && typeof target.scrollIntoView === 'function') {
      target.scrollIntoView({ block: 'start' })
    }
  }, [location.hash, doc.id])

  return (
    <div className="mx-auto w-full max-w-6xl px-4 py-12 sm:px-6">
      <header className="border-b border-border pb-8">
        <Badge variant="warning" dot>
          {PENDING_LEGAL_BADGE}
        </Badge>
        <h1 className="mt-4 text-3xl font-semibold tracking-tight text-ink sm:text-4xl">
          {doc.title}
        </h1>
        <p className="mt-3 max-w-2xl text-sm leading-relaxed text-ink-secondary">{doc.summary}</p>
        <div className="mt-5 rounded-xl border border-warning bg-warning-soft p-4">
          <p className="text-sm font-medium text-ink">本页尚未定稿</p>
          <p className="mt-1 text-xs leading-relaxed text-ink-secondary">
            {LEGAL_PLACEHOLDER_NOTICE}
          </p>
        </div>
      </header>

      <div className="mt-8 grid gap-8 lg:grid-cols-[16rem_minmax(0,1fr)]">
        {/* min-w-0 + overflow-x-auto：与 DocsPage 同款，章节条在手机端横向
            滑动，不会把整页视口撑破（G19）。 */}
        <nav aria-label={`${doc.title}章节`} className="min-w-0 lg:sticky lg:top-24 lg:self-start">
          <ol className="flex min-w-0 gap-1 overflow-x-auto pb-2 lg:flex-col lg:overflow-visible lg:pb-0">
            {doc.sections.map((section, index) => (
              <li key={section.id} className="shrink-0 lg:shrink">
                <a
                  href={`#${section.id}`}
                  className="flex w-full items-center gap-2 whitespace-nowrap rounded-lg px-3 py-2 text-sm text-ink-secondary transition-colors duration-fast hover:bg-surface-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                >
                  <span className="font-mono text-xs text-ink-muted">
                    {String(index + 1).padStart(2, '0')}
                  </span>
                  {section.title}
                </a>
              </li>
            ))}
          </ol>
        </nav>

        <article className="min-w-0">
          <div className="rounded-2xl border border-border bg-surface p-5 shadow-card sm:p-7">
            <p className="text-xs text-ink-muted">
              共 {doc.sections.length} 节，正文均待法务定稿；标题已按定稿目录确定。
            </p>
            <div className="mt-4 divide-y divide-border">
              {doc.sections.map((section) => (
                <section key={section.id} id={section.id} className="scroll-mt-24 py-5 first:pt-0">
                  <h2 className="text-lg font-semibold tracking-tight text-ink">{section.title}</h2>
                  <p className="mt-2 text-sm leading-relaxed text-ink-secondary">{section.body}</p>
                </section>
              ))}
            </div>
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-3 text-xs">
            {siblings.map((item) => (
              <Link
                key={item.to}
                to={item.to}
                className="font-medium text-primary hover:text-primary-hover"
              >
                {item.label}
              </Link>
            ))}
            <Link to={ROUTES.docs} className="font-medium text-primary hover:text-primary-hover">
              开发者文档
            </Link>
          </div>
        </article>
      </div>
    </div>
  )
}
