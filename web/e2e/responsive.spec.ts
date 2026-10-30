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

/**
 * 首屏高度护栏：1366×768 下首页必须能看到下一区块（WHY YUNSHU）的标题。
 *
 * 回归背景：用户两次反馈「首页上方还有空白」。hero 顶部的 `py-16 / lg:py-24`
 * 在 lg 下上下各留 96px，把约 500px 的内容撑到 646px，标题顶部被推到 791px；
 * 1366×768 是本机与多数笔记本的常见档位，折叠线在 768px——标题原本落在折
 * 叠线以下 23px，用户看到的就是「一屏空白，还得往下滚」。收紧到
 * `py-12 / lg:py-16` 后标题顶部 707px，进到折叠线内，余量 61px。
 *
 * 为什么单独一条用例、不用 `test.use`：本文件上面的矩阵测的是**宽度**
 * （横向溢出），这一条测的是**高度**（纵向首屏可见性），两件事混在一个矩阵
 * 里会让失败原因变模糊；`setViewportSize` 也避免把 1366×768 传染给其他用例。
 *
 * 余量只有 61px，hero 里再加约 60px（多一行文案、多一个 badge）就会静默回退，
 * 而宽度矩阵不会红——所以这条断言必须存在。失败时不要调宽阈值，回到
 * `LandingPage.tsx` 收紧 hero 的 `py` 与列内 `mt-*`。
 */
test('首屏：1366×768 下首页能看到下一区块标题', async ({ page }) => {
  await page.setViewportSize({ width: 1366, height: 768 })

  await page.goto('/', { waitUntil: 'load' })
  try {
    // 模型/价格数据填充后才出现数据条，hero 高度在此之后才最终确定。
    await page.waitForLoadState('networkidle')
  } catch {
    // 与上面宽度用例一致：某些代理环境下 networkidle 不触发，退回 load 后的布局。
  }

  interface FirstScreenProbe {
    /** 下一区块标题（capabilities 的 h2）顶部相对视口的纵坐标。 */
    titleTop: number | null
    headerBottom: number | null
    heroTop: number | null
    heroHeight: number | null
    innerWidth: number
    innerHeight: number
  }

  const probe: FirstScreenProbe = await page.evaluate(async () => {
    if (document.fonts?.ready) {
      await document.fonts.ready
    }
    const topOf = (selector: string): number | null => {
      const element = document.querySelector(selector)
      return element ? Math.round(element.getBoundingClientRect().top) : null
    }
    const header = document.querySelector('header')
    const hero = document.querySelector('section[data-section="hero"]')
    return {
      titleTop: topOf('[data-section="capabilities"] h2'),
      headerBottom: header ? Math.round(header.getBoundingClientRect().bottom) : null,
      heroTop: topOf('section[data-section="hero"]'),
      heroHeight: hero ? Math.round(hero.getBoundingClientRect().height) : null,
      innerWidth: window.innerWidth,
      innerHeight: window.innerHeight,
    }
  })

  expect(
    { width: probe.innerWidth, height: probe.innerHeight },
    '视口必须是 1366×768，否则首屏断言无意义',
  ).toEqual({ width: 1366, height: 768 })

  if (probe.titleTop === null) {
    throw new Error('首页找不到 [data-section="capabilities"] h2，首屏护栏无从度量')
  }

  const overflow = probe.titleTop - probe.innerHeight
  expect(
    probe.titleTop,
    `1366×768 首屏看不到下一区块标题：「从接入到计费」h2 顶部=${probe.titleTop}px，`
      + `折叠线=${probe.innerHeight}px，掉到折叠线以下 ${overflow}px`
      + `（header 底边=${probe.headerBottom}px、hero 顶部=${probe.heroTop}px、`
      + `hero 高=${probe.heroHeight}px）。请收紧 LandingPage 的 hero py / 列内 mt-*，`
      + '不要把这条用例的阈值放宽。',
  ).toBeLessThan(probe.innerHeight)
})
