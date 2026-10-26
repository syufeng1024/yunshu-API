import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it } from 'vitest'
import { SiteLayout } from './SiteLayout'

function renderLayout() {
  return render(
    <MemoryRouter>
      <Routes>
        <Route element={<SiteLayout />}>
          <Route index element={<div>首页内容</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
}

describe('SiteLayout', () => {
  it('A23: footer carries product, docs, agreement and filing groups', () => {
    renderLayout()

    const headings = screen.getAllByRole('heading', { level: 2 }).map((node) => node.textContent)
    expect(headings).toEqual(expect.arrayContaining(['产品', '文档', '协议', '备案信息']))

    for (const name of ['协议', '备案信息']) {
      const heading = screen.getByRole('heading', { level: 2, name })
      const group = heading.closest('div')
      expect(group?.querySelectorAll('a')).toHaveLength(0)
    }
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
