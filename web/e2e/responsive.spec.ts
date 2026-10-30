import { expect, test } from '@playwright/test'

/**
 * G19: 官网七个公开页面在移动/平板宽度下不得产生横向溢出。
 *
 * 回归背景：首页 hero 与文档页两处 grid/flex 子项缺少 `min-w-0`，被内部的
 * `<pre>` 代码块与 `whitespace-nowrap` 章节列表的 min-content 宽度撑破列宽，
 * 再由外层 `overflow-hidden` 静默裁掉——肉眼看到的是「右侧内容被切掉」，
 * 而浏览器的真实度量是 `documentElement.scrollWidth` 远大于视口。因此断言
 * 必须落在布局盒模型上，不能只看元素可见性。
 *
 * 两条断言对每个路由都成立：
 *  1. `documentElement.scrollWidth <= window.innerWidth + 1`（1px 容差用于
 *     子像素舍入，避免偶发假红）；
 *  2. 若页面存在 `h1`，其右边缘不得越过视口右边界——首页 hero 的 `h1` 被裁
 *     时 `scrollWidth` 可能因外层 `overflow-hidden` 仍然是 390，只有量
 *     `h1` 自己的 rect 才抓得到。
 *
 * 两档视口都验：390×844 手机，以及 768×1024 平板——`/docs` 的章节列表在
 * min-content 撑破时连 768 都会越界（修复前实测 scrollWidth=1025），只守 390
 * 会漏掉这一档。控制台/后台按产品决策只做桌面端，不在本用例范围内。
 *
 * 这些用例在 CI 里也必须真的执行，因此**没有** skip 条件：
 * `playwright.config.ts` 在非外部栈模式下会自起 5180 的 vite dev server 与
 * 8081 的 controld（CI 有 MySQL/Redis service），七个路由都是公开可渲染页面，
 * 数据接口不可用时也只是走 ErrorState，不改变布局断言的有效性。设置
 * E2E_BASE_URL 时则直连已启动的完整栈（如 8088 的 nginx + 生产构建）。
 */

interface ViewportSpec {
  /** 用例名里显示的档位说明。 */
  label: string
  width: number
  height: number
}

const VIEWPORTS: readonly ViewportSpec[] = [
  { label: '手机', width: 390, height: 844 },
  { label: '平板', width: 768, height: 1024 },
] as const

// 官网全部公开页面；控制台/后台按产品决策只做桌面端，不在本用例范围内。
// /terms 与 /privacy 是法律条款占位页（页脚协议三项的落地页），同样是公开页面，
// 章节导航在移动端是横向滑动的 `whitespace-nowrap` 列表，属于 G19 的高风险结构。
const PUBLIC_ROUTES = [
  '/',
  '/pricing',
  '/docs',
  '/integrations',
  '/login',
  '/terms',
  '/privacy',
] as const

interface LayoutProbe {
  /** 文档根元素的滚动宽度，即「整页真实内容宽度」。 */
  scrollWidth: number
  /** 视口内容宽度，用于给出可读的失败信息。 */
  innerWidth: number
  /** 首个 h1 的右边缘；页面没有 h1 时为 null。 */
  h1Right: number | null
}

/** 读一次布局度量。字体加载会改变行宽，所以先等 document.fonts 就绪。 */
async function probeLayout(page: import('@playwright/test').Page): Promise<LayoutProbe> {
  return page.evaluate(async () => {
    if (document.fonts?.ready) {
      await document.fonts.ready
    }
    const heading = document.querySelector('h1')
    return {
      scrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
      h1Right: heading ? heading.getBoundingClientRect().right : null,
    }
  })
}

for (const viewport of VIEWPORTS) {
  test.describe(`G19 · ${viewport.label} ${viewport.width}×${viewport.height}`, () => {
    test.use({ viewport: { width: viewport.width, height: viewport.height } })

    for (const route of PUBLIC_ROUTES) {
      test(`G19: ${route} 在 ${viewport.width}px 下不产生横向溢出`, async ({ page }) => {
        try {
          await page.goto(route, { waitUntil: 'load' })
          // 首页与 /docs 的价格/模型数据由接口异步填充，只有数据到位后的布局
          // 才是用户实际看到的布局；网络静默即视为渲染已稳定。
          await page.waitForLoadState('networkidle')
        } catch {
          // networkidle 在某些代理环境下可能不触发；此时退回 load 事件后的
          // 布局，仍然能覆盖 <pre> 与 nowrap 造成的结构性溢出。
        }

        expect(
          page.viewportSize(),
          `视口必须是 ${viewport.width}×${viewport.height}，否则断言无意义`,
        ).toEqual({ width: viewport.width, height: viewport.height })

        const probe = await probeLayout(page)

        expect(
          probe.scrollWidth,
          `${route} 在 ${viewport.width}px 下 documentElement.scrollWidth=${probe.scrollWidth}，超出视口 ${probe.innerWidth}`,
        ).toBeLessThanOrEqual(probe.innerWidth + 1)

        if (probe.h1Right !== null) {
          expect(
            probe.h1Right,
            `${route} 在 ${viewport.width}px 下 h1 右边缘=${Math.round(probe.h1Right)}px，超出视口 ${probe.innerWidth}px`,
          ).toBeLessThanOrEqual(probe.innerWidth + 1)
        }
      })
    }
  })
}
