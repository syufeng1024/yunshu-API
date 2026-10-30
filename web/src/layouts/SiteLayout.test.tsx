import { render, screen } from '@testing-library/react'
import { RouterProvider, createMemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { PENDING_BEFORE_LAUNCH } from '@/lib/placeholders'
import { SiteLayout } from './SiteLayout'

// SiteLayout renders <ScrollRestorer />, which requires a data router, so the
// harness uses createMemoryRouter rather than MemoryRouter.
function renderLayout() {
  const router = createMemoryRouter(
    [
      {
        element: <SiteLayout />,
        children: [{ index: true, element: <div>首页内容</div> }],
      },
    ],
    { initialEntries: ['/'] },
  )
  return render(<RouterProvider router={router} />)
}

/** 取页脚某个分组（h2 标题）下的 div 容器。 */
function footerGroup(name: string): HTMLElement {
  const heading = screen.getByRole('heading', { level: 2, name })
  const group = heading.closest('div')
  if (!group) throw new Error(`页脚分组「${name}」未找到`)
  return group as HTMLElement
}

describe('SiteLayout', () => {
  it('A23: footer carries product, docs, agreement and filing groups', () => {
    renderLayout()

    const headings = screen.getAllByRole('heading', { level: 2 }).map((node) => node.textContent)
    expect(headings).toEqual(expect.arrayContaining(['产品', '文档', '协议', '备案信息']))
  })

  it('A24: footer agreement group links terms / privacy / service-terms', () => {
    renderLayout()

    // 协议三项必须是真链接：用户协议 → /terms、隐私政策 → /privacy、
    // 服务条款 → /terms#service-terms（该节在 TermsPage 里带 id）。
    const links = Array.from(footerGroup('协议').querySelectorAll('a'))
    expect(links.map((link) => link.getAttribute('href'))).toEqual([
      '/terms',
      '/privacy',
      '/terms#service-terms',
    ])
    expect(links.map((link) => link.textContent)).toEqual(['用户协议', '隐私政策', '服务条款'])
  })

  it('A24: footer filing group states what is missing without inventing numbers', () => {
    renderLayout()

    const group = footerGroup('备案信息')
    // 资质由需求方另行提供：只能标注待补，不能填任何具体证号。
    expect(group.querySelectorAll('a')).toHaveLength(0)
    expect(group.textContent).toContain(PENDING_BEFORE_LAUNCH)
    expect(group.textContent).toContain('ICP 备案号')
    expect(group.textContent).toContain('增值电信业务许可证')
    expect(group.textContent).toContain('软件著作权')
    expect(group.textContent).not.toMatch(/ICP\s*备\s*\d/i)
  })

  it('keeps the brand link labelled and routes the header CTA to registration', () => {
    renderLayout()

    expect(screen.getAllByRole('link', { name: '云枢 API 首页' })).toHaveLength(2)
    expect(screen.getByRole('link', { name: '免费开始' })).toHaveAttribute(
      'href',
      '/login?tab=register',
    )
  })
})
