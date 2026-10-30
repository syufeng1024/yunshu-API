import { expect, test } from '@playwright/test'

/**
 * 页脚「协议」三项的可点性验收。
 *
 * 回归背景：这三项原来是裸 `<li>`，肉眼是链接、鼠标点不动。只断言 `href`
 * 并不能证明它真的能跳转（react-router 的 `<Link>` 会被劫持成客户端导航，
 * 而错误的嵌套或 `pointer-events` 会让点击落空），所以这里**真实点击**，然后
 * 断言 URL 与落地页内容：
 *
 *   用户协议 → `/terms`，h1 含「用户协议」
 *   隐私政策 → `/privacy`，h1 含「隐私政策」
 *   服务条款 → `/terms#service-terms`，且 `#service-terms` 这一节真实存在且可见
 *
 * 第三条是重点：服务条款不是独立页面，而是用户协议中的一节，链接带 hash。
 * playwright 的 `toBeVisible()` 判的是元素有可见盒模型，不要求在视口内，因此
 * 这里验的是「那一节真的渲染出来了」，与滚动位置无关。
 *
 * 三条用例互相独立：每条都从首页重新开始，避免第二条依赖第一条的落点——页面
 * 之间是 SPA 客户端跳转，页脚始终在 DOM 里，很容易写成「上一条点完后接着点」，
 * 那样一旦首条失败，后面两条的失败原因就会失真。
 */

/** 页脚容器；协议分组只在页脚出现，用它收窄定位，避开正文里的同名链接。 */
function footer(page: import('@playwright/test').Page) {
  return page.locator('footer.site-footer')
}

test('页脚「用户协议」点击后进入 /terms 并渲染用户协议', async ({ page }) => {
  await page.goto('/', { waitUntil: 'load' })

  await footer(page).getByRole('link', { name: '用户协议', exact: true }).click()

  await expect(page).toHaveURL(/\/terms$/)
  await expect(page.getByRole('heading', { level: 1 })).toContainText('用户协议')
})

test('页脚「隐私政策」点击后进入 /privacy 并渲染隐私政策', async ({ page }) => {
  await page.goto('/', { waitUntil: 'load' })

  await footer(page).getByRole('link', { name: '隐私政策', exact: true }).click()

  await expect(page).toHaveURL(/\/privacy$/)
  await expect(page.getByRole('heading', { level: 1 })).toContainText('隐私政策')
})

test('页脚「服务条款」点击后落到 /terms#service-terms 且该节可见', async ({ page }) => {
  await page.goto('/', { waitUntil: 'load' })

  await footer(page).getByRole('link', { name: '服务条款', exact: true }).click()

  await expect(page).toHaveURL(/\/terms#service-terms$/)
  await expect(page.getByRole('heading', { level: 1 })).toContainText('用户协议')

  const section = page.locator('#service-terms')
  await expect(section).toBeVisible()
  await expect(section).toContainText('服务条款')
  // 章节正文是占位文案，不得出现看起来像真实条款的内容。
  await expect(section).toContainText('本节内容待定稿')
})
