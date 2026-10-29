# T37 · GWT 剩余 4 条口径收口（G-02 / G-05 / G-09 / G-12）

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T37 |
| 阶段 | 6 · 质量与交付（收口） |
| 优先级 | P1（门槛 2 要求 G-01–G-12 全 ✅，当前 8/12） |
| 依赖 | T35（G-03/G-07 已收口）、T23 |
| 交付根 | PRD 附录 D、`internal/acceptance/gwt_test.go`、`web/e2e/`、`docs/tasks/acceptance/matrix-gwt.md` |
| 状态 | ✅ 通过（2026-10-29；GWT 8/12 → 12/12） |
| 派单方式 | 分段：段 1 = 三条文档裁定；段 2 = G-12 的 Playwright DOM 断言 |

## 0. 需求方裁定（2026-10-29，按此执行，不再讨论）

| 项 | 裁定 |
| :--- | :--- |
| G-02 | **以实现为准，改文档**：`est` 同时含 10% 安全余量与 1.2 预扣比（`6000 × 1.10 × 1.20 = 7920`），PRD 里写的 `7,200` 是漏算了安全余量的旧数值。准入仍只比 `raw_est`，行为不变 |
| G-05 | **以实现为准，改文档**：价格历史采用「同一行 `version` 递增 + `t_admin_audit_log` 留旧值」，不再要求独立旧版本行；历史账单金额由 `t_usage_record` 固化，不回溯 |
| G-09 | **登记 V1.0 不做**：试用积分 24 小时发放延迟不实现（现为立即发放）；防刷边界（设备指纹 / 手机号 / IP / 同账号）保持现状 |
| G-12 | **补实现级断言**：用 Playwright 在真实站点验证 `/pricing` 展示的价格随接口变化 |

## 1. 段 1：三条文档裁定

### 1.1 G-02（`docs/_API 中转站 · 需求文档 V1.2.md:241` 与 G-02 段 `:1356` 起）

- 先读 `internal/gw/p09_estimate.go` 的 `safetyBP=11000` / `preholdBP=12000` 与 `internal/config/key.go` 的默认值，**确认 1.10 × 1.20 = 1.32**；
- 把 G-02 里 `est 7,200` 改为 `est 7,920（= 6,000 × 1.10 安全余量 × 1.20 预扣比）`，并加 `（勘误 ERR-09，2026-10-29）` 标记；
- 若 §5 / 其它章节还有 `7,200` 的同源数值，**逐处核对该处上下文再改**；不属于 G-02 口径的一律不动。

### 1.2 G-05（PRD `:1431` G-05 段）

- 把「旧价格独立成行、可按 `price_rule_id` 回溯」这类表述改为实际实现：**同一 `t_price_rule` 行 `version` 递增，旧值写 `t_admin_audit_log`（before/after）；历史账单金额由 `t_usage_record.consumed_micro` 固化，调价不回溯**；
- 加 `（勘误 ERR-10，2026-10-29）`；
- 不改 G-05 的验收结论（「调价不回溯历史账单」这条仍然成立且已测）。

### 1.3 G-09（PRD `:1499` G-09 段）

- 在 G-09 末尾加一条明确登记：**「V1.0 不实现 24 小时发放延迟：试用积分审批通过后立即发放；防刷依赖设备指纹 / 手机号 / IP 段 / 同账号四重去重」**，加 `（登记 ERR-11，2026-10-29，需求方裁定不做）`；
- 不改其他验收点。

### 1.4 同步三处

| 文件 | 改什么 |
| :--- | :--- |
| `internal/acceptance/gwt_test.go` | G-05、G-09 两处 `t.Logf("G-0x DEVIATION: …")` 删掉，注释改为「PRD 已按 ERR-10 / ERR-11 勘误/登记」；**断言一行不改** |
| `docs/tasks/acceptance/matrix-gwt.md` | G-02 / G-05 / G-09 → ✅，实测值写清「以实现为准 + 勘误编号」 |
| `docs/tasks/delivery/errata.md` + `deviations.md` | 新增 ERR-09 / ERR-10 / ERR-11；D-06 / D-11 / D-12 标关闭 |

## 2. 段 2：G-12 的 Playwright 断言

- 目录 `web/e2e/`（若不存在则新建）+ `web/playwright.config.ts`（若不存在）：`testDir: './e2e'`，`baseURL` 取 `process.env.E2E_BASE_URL ?? 'http://127.0.0.1:8088'`，`use.channel = 'chrome'`，单 worker（避免并发打同一栈）。
- 新增 `web/e2e/pricing-price.spec.ts`：打开 `/pricing` → 读「标准分组」价格表某模型的输入价文本 → 与 `GET /api/public/models` 返回的同一模型价格**换算后一致**（换算函数复用 `web/src/lib/site.ts` 里的现有实现，不要重写一份）。
- 断言必须能失败：若页面显示与接口不一致（例如把页面数字改一位），该用例必须红——**交付总监会做反证**。
- 在 `docs/tasks/acceptance/matrix-gwt.md` 的 G-12 行写明运行命令：`cd web && E2E_BASE_URL=http://127.0.0.1:8088 npx playwright test e2e/pricing-price.spec.ts`。

## 3. 硬约束

1. **不得改断言、不得改测试名**；删日志 ≠ 放宽。
2. PRD 只做上述三处裁定相关改动，**不得顺手改其它措辞/数值/结构**；每处必须带勘误标记。
3. 不新增 npm 依赖（`@playwright/test` 已装）；不改 `/v1/*`、不改业务代码逻辑。
4. `bash scripts/check-eol.sh` 退出码 0；不 commit / 不 push。

## 4. 验收口径

| # | 查什么 | 通过标准 |
| :--- | :--- | :--- |
| 1 | PRD 改动可枚举 | `git diff --stat` 该文件的行数 == 三处裁定的实际改动数；每处都有 `ERR-09/10/11` 标记；无其它行变动 |
| 2 | 断言未改 | `git diff internal/acceptance/gwt_test.go` 中 `t.Fatalf` 零改动；`grep -c DEVIATION` == 0 |
| 3 | GWT 全绿 | `go test ./internal/acceptance -run TestGWT_G` → 12/12 PASS |
| 4 | G-12 有效 | `npx playwright test e2e/pricing-price.spec.ts` PASS；我改页面一位数字后必须 FAIL（反证） |
| 5 | 矩阵 | `matrix-gwt.md` 12 行全 ✅ |
| 6 | 回归 | `EOL=0`；既有 vitest 48 文件全绿（`--no-cache`） |

## 5. 交付记录

### 段 1 · 三条文档裁定

派单 14:17，交付 14:20（55 turns / 195s）。

| 项 | 结论 |
| :--- | :--- |
| G-02 | 先核实 `safetyBP=11000` × `preholdBP=12000` = **1.32**，再把 PRD 三处 `7,200` → `7,920` 并带 ERR-09 |
| G-05 | PRD 改为「同一 `t_price_rule` 行 `version` 递增 + 审计留存旧值；历史金额由 `t_usage_record` 固化」ERR-10 |
| G-09 | PRD 登记「V1.0 不实现 24h 延迟」ERR-11 |
| 测试 | 删 G-05/G-09 两行 DEVIATION 日志，`t.Fatalf` **零改动**；`grep -c DEVIATION` = 0 |

**实现方顶回我一个错**：我卡里要求把门槛行写成 12/12，但段 1 不含 G-12，写 12/12 会与同批矩阵自相矛盾——它按实况写 11/12。**这是对的**。

### 尾段 · G-02 同源口径对齐（我追加的一段）

PRD 里还有两处同源陈述与新口径矛盾，已改：`:241`「含 20% 余量」→「10% 安全余量 × 1.2 预扣比」；G-02 Given `est = 63,000` → `69,300`（并把「token 数已含 safety_bp」改成「安全余量由 `SafeInputTokens` 单独体现」）。**另剩 2 处（`:1872`、`:2308`）不属 G-02 场景，已写入 `errata.md` 待授权**。

### 段 2 · G-12 Playwright DOM 断言

派单 14:21，交付 14:25（48 turns / 248s）。

| 项 | 结论 |
| :--- | :--- |
| 落点 | `web/e2e/pricing-price.spec.ts`；`playwright.config.ts` 扩展（`E2E_BASE_URL` 可选、CI 下不强制 channel、外部栈时不启 dev server） |
| 断言 | 首页「模型价格表」首行输入单价 vs `/api/public/models` 同模型价格，期望值用页面同一个 `formatPricePerK` 计算 |
| 反证 | 把 `PriceTable` 的价格乘 10 → 用例红（`1.5` vs `0.15`）；还原后绿，`git diff` 为空 |
| 我修的 | 配置里默认 `baseURL` 被写成 8088（而 `webServer` 仍启 5180），会让原有安全 e2e 指向错误地址；已改回「默认 5180，`E2E_BASE_URL` 才切外部栈」 |

### 自检复跑

`go test ./internal/acceptance -run TestGWT_G` → **12/12 PASS**；`npx playwright test e2e/pricing-price.spec.ts` → **1 passed**；`TSC_OK`；`EOL=0`。

### 结果

`matrix-gwt.md` **12/12 ✅**；`go-live.md` 门槛 2 ❌→✅，通过数 **4/8**（剩门槛 3 码表、4 对账演练、6 开放 P0、7 备份恢复）。

### 遗留

| # | 项 | 处置 |
| :--- | :--- | :--- |
| 1 | PRD `:1872`、`:2308` 两处 est 余量口径仍是旧说法 | 已写进 `errata.md` 的 ERR-09 影响面补记，待授权再改 |
| 2 | `gwt_test.go` 里 G-12 的 `t.Logf` 仍写「/pricing DOM 验证推迟到 Playwright 段」 | 现在该段已存在，日志过期；删它属 `internal/acceptance` 改动，本轮未授权，建议下一轮清 |
| 3 | `e2e/pricing-price.spec.ts` 未纳入 CI workflow | 需 `E2E_BASE_URL` 指向一套跑起来的栈；要不要进 CI 是运维决定 |

