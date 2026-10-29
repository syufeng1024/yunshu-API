# T29 · SPA 路由滚动复位与 A 项布局收口

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T29 |
| 阶段 | 6 · 质量与交付（交互缺陷收口） |
| 优先级 | P1（A 项是 P0，但当前不可复现，本卡负责给出可证伪的结论） |
| 依赖 | T28（A 项未复现、G10 由此登记） |
| 交付根 | `web/src/router.tsx`、`web/src/layouts/`、`web/src/components/` |
| 状态 | ✅ 通过（2026-10-29；G10 修复，A 结论为「非同根因、仍不可复现」） |

## 1. 目标

两件事，按顺序做：

1. **修 G10（确定缺陷）**：SPA 前进导航不复位滚动位置。从已滚动的页面点击导航进入新页，新页保留旧 `scrollY`（实测 `/console/playground` 开局 `scrollY=444`），用户看到的是「页面从中间开始」。
2. **给 A 项一个可证伪的结论**：T28 的 P0「PageDown 后上方约 500px 全白」在本机（Chrome 156 / 1408×672）**两次独立测量都未复现**（playground 顶部命中 `TEXTAREA`、pricing 五个 section 间距全 0）。本卡要求用「带残留 scrollY 进入 + PageDown」这条真实路径重测，判断 **A 与 G10 是否同一根因**，给出结论而不是猜测。

## 2. 必读

| 文档 / 文件 | 要点 |
| :--- | :--- |
| `web/src/router.tsx` | `createBrowserRouter`（数据路由，react-router-dom 6.30.6），三个顶层 `element`：`SiteLayout` / `ConsoleLayout`(含 `RequireAuth`) / `AdminLayout`；**全仓没有挂载 `<ScrollRestoration />`** |
| `web/src/App.tsx` | 只有 `RouterProvider`；没有全局滚动逻辑 |
| `web/src/layouts/ConsoleLayout.tsx` | `:28` `min-h-screen`、`:29` `aside sticky md:h-screen`、`:75` `min-w-0 flex-1` |
| `web/src/layouts/SiteLayout.tsx`、`AdminLayout.tsx` | 另两套布局，需一并挂载 |
| `docs/tasks/T28-实测缺陷修复与待办总账.md` §2A、§9 | A 项现象、已做过的两次测量结论 |
| `docs/tasks/BACKLOG.md` G10 | 本卡的来源与判据 |

## 3. 决策（不在卡内重新讨论）

| 编号 | 决策 |
| :--- | :--- |
| D-26 | **用数据路由原生的 `<ScrollRestoration />`，不自己写 `useEffect(() => window.scrollTo(0,0), [pathname])`**。原生组件语义正确：PUSH/REPLACE 回顶部、POP（浏览器前进/后退）恢复位置。三套 layout 都要覆盖，禁止只修控制台。**不新增依赖**（react-router 已内置）。 |

## 4. 交付物清单

| # | 路径 | 内容 |
| :--- | :--- | :--- |
| 1 | `web/src/components/ScrollRestorer.tsx`（新建） | 薄封装：渲染 `<ScrollRestoration />`，附一行注释说明 D-26 与「为什么不用 effect」 |
| 2 | `web/src/layouts/SiteLayout.tsx`、`ConsoleLayout.tsx`、`AdminLayout.tsx` | 各挂一个 `<ScrollRestorer />`（位置：布局根节点内、最外层可渲染处） |
| 3 | `web/src/components/ScrollRestorer.test.tsx`（新建） | 用 `createMemoryRouter` + `RouterProvider` 挂两页，断言：导航到新页后 `window.scrollY === 0`；`history.back()` 后位置被恢复（可用 `window.scrollTo` 打桩记录调用） |
| 4 | 若 A 项复现且根因在布局 | 按根因最小改动修（见 §5 硬约束 1）；**未复现则不动布局源码** |
| 5 | 本卡 §8 交付记录 | 由交付总监回填，实现方不要改 |

## 5. 硬约束

1. **禁止用加 padding / 改高度掩盖**。A 项若复现，必须先给出「哪个容器、多高、为什么」的证据再改；若判断要动 `ConsoleLayout` 的 `min-h-screen` / `sticky`，先报影响面。
2. 只允许碰 §4 列出的文件；**禁止**改 `internal/**`、`deploy/**`、`api/**`。
3. 不得为了过门禁改测试名、拆字符串绕过 grep、或把断言降级（T20 教训）。
4. `bash scripts/check-eol.sh` 退出码必须 0。
5. 不新增 npm 依赖。
6. **不要 git commit / git push**，改动留在工作区。

## 6. 自检命令（已按本机修正，卡面路径为准）

```bash
cd /d/codes/yunshu-api/web
npx tsc --noEmit && echo TSC_OK
npx vitest run --reporter=dot 2>&1 | tail -8
cd /d/codes/yunshu-api && bash scripts/check-eol.sh; echo EOL=$?
```

## 7. 验收口径（交付总监独立复跑）

| # | 查什么 | 通过标准 |
| :--- | :--- | :--- |
| 1 | G10 修复 | Playwright（Chrome，1408×672）：① 在 `/console/keys` 手动 `scrollTo(0,400)` → 点侧栏「调试台」→ 新页 `scrollY === 0`；② 直接打开 `/console/playground` → `scrollY === 0`；③ 上述路径下按一次 `PageDown`，视口顶端 `elementFromPoint` 命中内容而非空白，空白带 ≤ 24px |
| 2 | 后退语义 | 从 `/console/keys`（滚到 400）→ 进调试台 → `history.back()` → 回到 keys 且位置恢复（允许 ±50px） |
| 3 | 覆盖三套布局 | `grep -rn "ScrollRestorer" web/src/layouts/` 命中 3 个文件 |
| 4 | A 项结论 | 回执必须明确回答「A 与 G10 是否同一根因」，并给出带残留 scrollY 场景下的实测数值；不得只写「未复现」 |
| 5 | 回归 | `TSC_OK`、vitest 全绿（文件数/用例数不低于 46/194）、`EOL=0` |

## 8. 交付记录

### 完成回执（已归档，2026-10-29）

派单 10:44，交付 10:54（56 turns / 592s / $2.17）。实现方未提交 git，改动由交付总监复核后提交。

| 项 | 实现方自报 | 交付总监独立复核 | 裁定 |
| :--- | :--- | :--- | :--- |
| G10 根因 | `router.tsx:35` 用 `createBrowserRouter` 建数据路由，但三套 layout 都没挂 `<ScrollRestoration />`，react-router 因此不接管滚动 | 读码确认：全仓仅 `ScrollRestorer.tsx` 引用 `ScrollRestoration`，三套 layout 各挂 1 处（`ConsoleLayout.tsx:30`、`SiteLayout.tsx:45`、`AdminLayout.tsx:32`） | ✅ |
| G10 修复 | 新增 `ScrollRestorer.tsx` 薄封装；三套 layout 挂载；新增 2 个用例（PUSH 回顶 / POP 恢复） | 重建 web 镜像后在 8088 实测：`/console/usage` 滚到 400 → 点侧栏「在线调试台」→ **`scrollY=0`**；`goBack()` → **`scrollY=400`**（位置恢复）；`/docs` 滚到 400 → 点「定价」→ **`scrollY=0`** | ✅ |
| A 项结论 | **与 G10 非同根因**：8 组「残留 scrollY + PageDown」实测空白 0–13px；浏览器会先把 `scrollY` 钳到 `maxScroll`（实测 672 档 maxScroll=1299、停在 588），滚动只把内容上移，不可能在内容上方造出空白；G10 的真实症状是「页面从中段开始」，与 A 的「整体下沉、上方全白」方向相反 | 我在重建后的生产包上复测同一条路径：`scrollY=0` → PageDown 后 `scrollY=588`、视口 y=2 命中 **`TEXTAREA`**、`firstTop=-564`（内容在视口上方，非空白）；`/pricing` 同理命中站点头部、`firstTop=-523`。**未见任何 >24px 空白带** | ✅ 结论成立 |

### 自检复跑（交付总监执行）

| 检查 | 结果 |
| :--- | :--- |
| `npx tsc --noEmit` | `TSC_EXIT=0` |
| `npx vitest run` | **47 文件 / 196 用例**全通过（基线 46/194，本卡 +1 文件 / +2 用例） |
| `npm run build` + 重建 `yunshu-web:local` | 成功，`WEB=200` |
| `bash scripts/check-eol.sh` | `EOL=0` |

### 偏离（成立，已接受）

| # | 偏离 | 裁定 |
| :--- | :--- | :--- |
| 1 | 改了卡面清单外的 `web/src/layouts/SiteLayout.test.tsx` | 成立：`<ScrollRestoration />` 在 `MemoryRouter` 下会抛 `useScrollRestoration must be used within a data router`，两例直接红。只换 harness（`MemoryRouter` → `createMemoryRouter`），断言逐字未改。**未采用「非数据路由静默降级」的绕法**，方向正确 |
| 2 | 单测 POP 用 `navigate(-1)` 而非 `history.back()` | 成立：`createMemoryRouter` 不经过 `window.history`，`navigate(-1)` 派发的是同一个 POP action，`<ScrollRestoration>` 正是按 action 判定恢复分支 |
| 3 | 验收口径①的 `/console/keys` 不可滚（`docH == innerHeight`，`maxScroll=0`） | 成立：当前种子数据下该页只有一屏，`scrollTo(0,400)` 只能停在 0，属**空真**。改用同布局的 `/console/usage`（`maxScroll=825`）作为真实来源，我独立复测时也走这条 |

### 遗留

| # | 项 | 处置 |
| :--- | :--- | :--- |
| 1 | **A 项（P0 布局空白带）在两档视口、四条路径下仍不可复现** | 四次独立测量（T28 两次 + 本卡两次，含生产包）空白带 ≤24px。**转 BACKLOG G13 挂起**，等用户提供浏览器版本 / 缩放 / 录屏路径 |
| 2 | `ScrollRestorer` 依赖数据路由 | 后续任何直接渲染三套 layout 的新测试必须用 `createMemoryRouter`，否则抛错。已在 `SiteLayout.test.tsx` 注释说明 |

