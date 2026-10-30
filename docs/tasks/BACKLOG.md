# 全局待办总账（BACKLOG）

> **这是跨卡待办项的唯一登记处。** 各卡 `§11 交付记录` 里「遗留」只写本卡范围内没做完的事；**凡是需要另外派卡、或者卡外才能解决的**，一律登记到本文件，并在来源列写清出处。
>
> 更新规矩：新增项用下一个 G 编号（不重用已关闭编号）；关闭项**不要删行**，把状态改成 ✅ 并在「关闭方式」里写提交号或后续卡号，保留追溯。
>
> 最后更新：2026-10-30

| # | 项 | 来源 | 性质 | 建议 / 关闭方式 | 状态 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| G1 | 镜像内 embed SQL 的 checksum 未校验 | T26 §10.4 | 工程守卫 | CI 构建镜像后跑一次 `go test ./internal/platform/migrate/`，防跨平台漂移（本地与 CI 构建出的镜像目前可能 checksum 不同） | ⬜ |
| G2 | `meta_only` 切回时历史正文不删（TDS 4318 / B-67「用户关闭即删」） | T25 §11.5 | 功能缺口 | 单独立项，涉及批量删除与审计；注意与 180 天保留期的清理逻辑不要互相干扰 | ⬜ |
| G3 | 正文 KEK 轮换后旧密文如何续读 | T25 §11.5 | 设计缺口 | 需先定 `body_key_id` 多版本方案，再改 `logq.sqlBodyStore` 的选 key 逻辑 | ⬜ |
| G4 | T13 缺 QA 独立验证 | 台账 | 流程欠账 | ✅ 2026-10-29 独立回归完成（`eafddb1`）：A1–A14 逐条对账，补 A3 状态守门与 A4 默认路径，A14 收紧 section 作用域；`-race` 全绿、覆盖率 65.2%→65.5%、测试函数 33→35，3 条反证实验确认断言真会红 | ✅ |
| G5 | T23 82 项功能点验收对照 | T23 卡 | 出文档 | 依赖 G2/G3 定稿后再做更准；产出 `docs/tasks/acceptance/` 七份矩阵 | ⬜ |
| G6 | T22 段 4 运维件（告警规则 / 备份脚本 / DR / 降级矩阵） | T22 卡 | 运维 | 需求方已明确「上线配置先不管」，暂缓 | ⏸ **（2026-10-29 更新：这是 Go/No-Go 门槛 4 与 7 的唯一依赖——不解挂，发版永远 No-Go；等需求方一句话）** | ⏸ |
| G7 | 邮件短信通知 / 一键导入 | T24 §11 | **需产品决策** | 等需求方定供应商与范围 | ⏸ |
| G8 | 枚举归一口径：`usage_source` 输出 `UPSTREAM_API`、`protocol_in/out` 输出 `OPENAI_COMPAT`，而 TDS §5.1 要求小写 `upstream` / `openai` | T22 段2 遗留 #3 | 一致性 | 二选一：改日志产出侧，或在消费端做映射。必须定一处为准，否则按 TDS 聚合会落空 | ⬜ |
| G9 | 未配 `VITE_API_BASE_URL` 时，调试台代码生成用 `window.location.origin` | T28 §8（原盘点结论修正） | 部署提醒 · **上线前必办** | **非代码缺陷**：`PlaygroundPage.tsx:174` 的 `apiBaseUrl()` 优先读环境变量，回退 origin 属正常设计。**上线清单必须加一条「配 `VITE_API_BASE_URL`」**，否则生成的示例代码域名会是错的。收口方式：在 `docs/ops-runbook.md` 上线检查清单固化该条，并在 `deploy/.env.stack` / `.env.example` 标注必填（2026-10-29 需求方确认：统一在「上线前」批量处理，不单独派卡） | ⬜ 上线前必办 |
| G10 | SPA 路由切换不复位滚动：从已滚动的控制台页点侧栏进入 `/console/playground`，`scrollY` 会保留（实测 444），新页从中间开始显示 | T28 §9 遗留 #2 | 交互缺陷（非 P0） | ✅ T29 修复：`ScrollRestorer`（数据路由原生 `<ScrollRestoration />`，D-26）挂到三套 layout；生产包实测 `scrollY` 400→0，`goBack()` 恢复 400 | ✅ |
| G11 | T13/A6 的「配置版本生效」只验证到 fund 层 hook 调用（测试自带 stub 做 `INCR cfg:ver`），真实 `boot.RouteSnapshot.Publish` 链路未独立验证 | T13 §12（G4 回执 A6） | 测试缺口 | ✅ 2026-10-29 T30 段2（`a5dd6e9`）：`internal/acceptance/g11_publish_test.go` 走真链路，断言 `cfg:ver` N→N+1、快照含新渠道、`cfg:invalidate` 消息；测试侧无 `Incr` | ✅ |
| G12 | 购买套餐会静默取消「次日生效」的提额：`plan_grant.go` 的 `UPDATE ... SET effective_to=now` 未限定 `effective_from <= now`，会把尚未生效的 `QUOTA_APPLY` 行直接关掉 | T13 §12（G4 回执遗留 #3） | **已确认的逻辑缺陷** | ✅ 2026-10-29 T30 段1（`239a233`）：关闭条件加 `effective_from <= now`（未来行一字不动）；在效提额改为「截断原行 + plan+1ms 续接行」，额度保留、三档升级；T1–T4 用例 + 反证验证 | ✅ |
| G13 | **A 项（P0 布局空白带）四次测量均未复现**：PageDown 后上方约 500px 全白；`/pricing` 标题与卡片间约 90px 空白 | T28 §9 / T29 §8 | **待外部信息** | 四次独立测量（T28 ×2、T29 ×2，含生产包、1408×672 与 1408×585 两档）空白带均 ≤24px；已排除 G10 滚动残留这条解释。**等用户提供**：Chrome 版本、页面缩放是否 100%、是否从已滚动页面点进来、可否录屏。信息到位再开卡，禁止无证据加 padding | ⏸ |
| G14 | `plan_grant.go` 捕获在效提额行时把 `daily_quota_micro` 直接 `Scan` 进 `int64`，历史数据若该列为 NULL 会 500 | T30 §8 遗留 #1 | 健壮性（低风险） | 改 `sql.NullInt64` 兜底 + 一条 NULL 用例；`EffectiveQuotaApply` 要求 approved 非空，实际触发概率极低 | ⬜ |
| G15 | 请求日志 `obslog.Record.ConfigVersion`（`internal/obs/log/record.go:72`）同样无生产者：全仓无 `obslog.Record{` 构造点，P30 日志行的该字段恒为 0 | T40-a §8 遗留 #2 | 可观测缺口（非资金，非门槛） | 与 P30 请求日志落地一并处理：构造 `obslog.Record` 时复用 `gw.Ctx.ConfigVersion`（T40-a 已在两个入口盖章，取现成值即可）。**不要另起一条 Redis 读取路径** | ⬜ |
| G16 | `scripts/dev.sh` 的 `check)` 目标里三条内联 grep 是 `scripts/lint.sh` 的过期副本，口径已漂移：`encoding/json` check 扫 `.` 且不排 `_test.go` → 17 命中（lint.sh 0）、`context.Background()` check 只排 `^cmd/` → 537 命中（lint.sh 0），该目标长期恒红而无人察觉（CI 真正跑的是 `lint.sh`） | 本次端口优先级收口（A1） | 工程守卫 | ✅ 2026-10-30（`2f73e1d`）：`check` 改为委派 `bash "$ROOT/scripts/lint.sh"`，删掉第二份口径与上轮的 `test-env_test.sh` 重复执行（lint.sh 第 11 条已调用），一处实现 | ✅ |
| G17 | `.gitattributes` 缺 `*.go text eol=lf`：本机 `core.autocrlf=true` 使 93 个 `.go` 为 `w/crlf`，`gofmt -l` 恰好只报这 93 个、零处真实格式问题（`git archive` 导出树 440 个 `.go` 的 gofmt 全干净）；另有 `scripts/check-eol.sh` 自身也是 `w/crlf`（index 是 LF，规范化后零 diff） | 本次端口优先级收口（顺带发现） | 工程守卫 | 加规则后 `git add --renormalize .`。**必须单独派卡**：改动 93 个文件行尾 + 全量回归 + 通知所有人重拉，不得塞进本次 bugfix | ⬜ |
| G18 | `docs/tasks/acceptance/matrix-nonfunctional.md:7` 记录那批非功能验收的测试库是「原生 MySQL `127.0.0.1:3306`」。实测该端口是 **MySQL 8.4.11**，而 GR-01 钉 8.0（容器 13306 = 8.0.46），故该批证据的版本基线存疑 | 本次端口优先级收口（顺带发现） | **举证有效性（高优先）** | ① ✅ 已完成：在该表新增「版本注记（2026-10-30 补）」行，保留 10-29 原文不改写，注明本机 3306 = 8.4.11 与钉版不一致，并逐类说明影响范围（Go 后端 ✅ 项已于 10-30 在 8.0.46 全量复跑通过 45 包；前端 ✅ 项未复跑、仍沿用旧记录；⚠️/❌ 项判定口径与版本无关）；`matrix-nonfunctional.md:120` 的 3306 事故记录已加「当时」限定。② ⬜ 待定：是否需要在 8.0 容器上**重做整批非功能测量**（含 `TestSoak` 200QPS×10min、备份恢复演练），取决于 §5 门槛 7 是否解挂 | 🟨 部分完成 |
| G19 | 前端 `npx vitest run` 在**受限沙箱**（本机 WorkBuddy 的 fs shim）下会**静默丢弃 2 个测试文件**：`src/api/console.test.ts`（5 用例）、`src/pages/console/RechargePage.test.tsx`（9 用例）。表现：报告 `46 passed (46)` + 2 个 `EPERM ... AppData\Local\Temp\...` unhandled error、exit 1 —— 看上去像「环境噪音」，实际后果是**少跑了 14 个用例**。把 `TMPDIR/TMP/TEMP` 指向可写目录（如仓库内 `.vitest-tmp`）后恢复 `48 passed / 211 passed`、exit 0（2026-10-30 实测） | 本次收口（前端补测） | 环境陷阱（非代码缺陷；CI 的 Linux runner 不复现） | ① 跑前端单测后**必须核对文件数**：`find web/src -name '*.test.ts*' \| wc -l` 应等于报告里的 `Test Files`，不等即被漏跑——只看「passed」会得到假绿；② 受限沙箱下用 `TMPDIR=<可写目录> npx vitest run`；③ 若要根治，可在 `web/vitest.config.ts` 把 cacheDir/deps 缓存固定到仓库内，但会在仓库里多出缓存目录，需评估 | ⬜ |

| G20 | `npm run check:copy` 在 main 上**恒红**：`web/src/components/ScrollRestorer.test.tsx:74` 的 `sessionStorage.clear()` 命中脚本的 "browser storage reference" 规则（该文件来自 `9d3d9ba`）。已用 `git archive HEAD` 导出 pristine 树复核：pristine 与工作区输出**逐字相同、唯一 1 条**，即非近期改动引入 | 合规占位收口时发现 | 工程守卫（规则过宽） | 二选一：① 给 `src/**/*.test.*` 加豁免（该规则本质是防生产代码裸用 storage，测试里为验证 G10 复位行为调用 `sessionStorage.clear()` 属合理）；② 让该断言改为只观察 `ScrollRestoration` 行为、不直接碰 storage。注意 `npm run verify` 含 `check:copy`，红着等于本地 verify 不可用 | ⬜ |

## 状态图例

| 标记 | 含义 |
| :--- | :--- |
| ⬜ | 待办，可派卡 |
| ⏸ | 挂起，等外部条件（产品决策 / 需求方裁定） |
| 🟨 | 进行中，已派卡 |
| ✅ | 已关闭（附提交号或卡号） |

## 与卡内「遗留」的分工

| 位置 | 记什么 |
| :--- | :--- |
| 卡面 `§11 交付记录` 的「遗留」 | **本卡范围内的**未完成项、已知限制、未验证项 |
| 本文件 | **跨卡的**、需要另行派卡的、卡外才能解决的 |

判据很简单：**如果关掉那张卡就再也找不到的事，写这里。**

## 上线前必办（Release Blocker 清单）

> 需求方 2026-10-29 确认：下列项**不单独派卡**，统一在「上线前」批量处理；上线前逐条打勾，未勾不得发版。

| # | 项 | 验收方式 | 状态 |
| :--- | :--- | :--- | :--- |
| **RB-1** | 生产构建注入 `VITE_API_BASE_URL`（否则调试台生成的示例代码域名回退成 `window.location.origin`） | ① 在 `deploy/.env.stack` 或 CI 构建参数注入生产 API 域名；② 生产包 grep 构建产物中存在该域名 | ⬜ 未做（OI-12 / G9） |
| **RB-2** | **用生产构建产物验证调试台「查看代码」** | 打开 `/console/playground` →「查看代码」，确认示例代码的 base_url 是生产 API 域名，**不是** `window.location.origin` | ⬜ 未做（OI-12） |
| **RB-3** | 内容安全外部适配器的数据处理协议（仅启用 `GW_POLICY_ENDPOINT` 时需要） | 确认允许把用户请求文本发往该供应商，并在隐私政策写明；未启用则跳过（关键词库仍生效） | ⬜ 待定 |
| **RB-4** | `GW_POLICY_ENDPOINT` / `GW_POLICY_TOKEN` / `GW_POLICY_TIMEOUT_MS` | 未启用留空（网关不构造适配器）；启用则注入，token 不得进日志 | ⬜ 待定 |
| **RB-5** | `CHANNEL_KEY_KEK` 与 DB / Redis 凭据 | 由部署环境注入，不落仓库、不进 argv | ⬜ 待定 |
| **RB-6** | 迁移与行尾校验 | `migrate status` 到 0026、`checksums.golden` 与仓库一致、`bash scripts/check-eol.sh` 退出 0 | ⬜ 待定 |

> **RB-1 / RB-2 是门槛 6 的唯一阻塞项（OI-12）**：两者都打勾后八条门槛齐备，方可将 Go/No-Go 改为 Go。逐条视图同步在 `docs/tasks/delivery/go-live.md` §四。

