# 实现偏差登记（deviations）

统计口径：初版 30 条（`matrix-82.md` 14 条 `⚠️` + `matrix-gwt.md` 7 条 + `matrix-codes.md` 9 条）。**2026-10-29 更新**：`matrix-82.md` 经 T32/T33/T34 收口后为 `⚠️ 7`（F2-1、F6-2、F8-1、F8-3、F20-4、F22-4、F22-6 已关闭，见文末「T34 关闭清单」），故本表有效条目为 **23 条**（82 项 7 + GWT 7 + 码表 9）。

排序：功能缺口（D-01–D-07）> 表示差异（D-08–D-14）> 覆盖缺口（D-15–D-30）。`⚠️` 计数 14 > T23 §5 门槛的 8，本表只完成「已登记」，不代表门槛达标。

| 编号 | 文档要求 | 实际实现 | 理由 | 影响 | 是否需回改文档 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| D-01 | F4-2：一键导入全程抓 DOM / URL / localStorage / 埋点，均无明文 Key。见 `matrix-82.md` F4-2 | `POST /api/v1/integrations/import-credentials` 未实现，验收用例断言 404；`GET /api/public/integration-links` 恒返空数组 | 「一次一密临时 Key」与「用户粘贴已有 Key」的产品方案未定（BACKLOG G7）；先按前端模板替换实现，未造后端假数据 | F4-2 只覆盖静态源码与 API，DOM 端到端未验；导入链路整体不可用，`❌` 部分见 `open-issues.md` OI-01 | 是（PRD D.3 标注 V1.0 未实现，或补齐产品方案后实现） |
| D-02 | F10-3：已用兑换码拒绝并说明原因；审核中心生成的 `REDEEM_BATCH` 可正常兑换。见 `matrix-82.md` F10-3 | 二次兑换返回 400 `invalid_request_body`，`CODE_USED` 只写审计不回传；`REDEEM_BATCH` 审批返回 409「提交入口尚未实现」，单据保持 `PENDING` | 兑换码拒绝码未进 `apierr` 注册表；审核中心到兑换批次的写入口未接线 | 客户端无法区分「码已用」与「body 非法」；审核中心生成的批次不能兑换 | 否（属实现缺口） |
| D-03 | F16-5：分组倍率降到成本线以下须拒绝并给差额；§6.4 套餐分支四步公式落地。见 `matrix-82.md` F16-5 | 地板只在价格写入 `pricing.go:838` 校验；分组端点 `group.go:119` 只校验倍率语法。`plan_min_margin_bp` / `max_bonus_ratio_bp` 仅有配置键，无实现与后台入口；`internal` 分组二次确认未实现 | 地板检查复用了价格写入路径的整数交叉相乘，未在分组写入路径重复实现 | 已配价模型可被事后调低分组倍率绕过地板，出现负毛利；`internal` 分组改倍率无二次确认 | **分组侧已修复**（T31 `a6f1317`）；**套餐分支仍缺**（无写入路径，OI-13） |
| D-04 | F20-4：提额通过后站内通知到达；发票 `ISSUED`、退款 `REFUNDED` / `REFUND_FAILED` 额外发邮件。见 `matrix-82.md` F20-4 | 提额通过路径不写站内通知，仅驳回写 `QUOTA_AUDIT`；`notify.go:13` 注明仓库无 SMTP 客户端，nil Mailer 只记 WARN，邮件实际不发 | 邮件/短信供应商未定（BACKLOG G7）；站内通知只接了驳回分支 | 用户不知道提额已通过；发票与退款状态变化无邮件触达 | 否（属实现缺口；邮件通道受产品决策阻塞） |
| D-05 | G-06：`30,000×0.6=18,000 < 26,667` 时分组端点拒绝保存。见 `matrix-gwt.md` G-06 | 同一用例在价格写入处断言拒绝（`actual=18000 required=26667 shortfall=8667`）；分组端点本身无地板校验 | 同 D-03 | GWT 断言通过但保护点与文档描述的位置不同；经分组端点改倍率仍可绕过 | **已关闭**（T31 `a6f1317` + G-06 补断言，矩阵转 ✅） |
| D-06 | G-09：体验金发放须满足「发放延迟满 24 小时」等四项前置校验，拒绝时指明缺哪一项。见 `matrix-gwt.md` G-09 | 24h 延迟未实现；四类命中共用一条不指明因素的拒绝文案；其余断言（同设备 1 次、7,000 积分、90 天）通过 | 拒绝不指明因素是为避免向攻击者泄露命中维度；24h 延迟未排期 | 反刷强度低于 PRD；正常用户被拒时无法自助定位原因 | 否（属实现缺口；拒绝文案口径需产品确认） |
| D-07 | F2-1：已登录点套餐卡跳充值中心并预选该档。见 `matrix-82.md` F2-1 | `PlanCards` 生成 `/console/recharge?plan=PRO`，但 `RechargePage` 不读取 `plan` 查询参数，进入后仍预选第一个可购档 | 充值页按 `plans[0]` 初始化，未接查询参数 | 用户需手动重选套餐，预付金额可能选错档 | 否（属实现缺口） |
| D-08 | G-03：`t_recharge_order` 仅 1 行且状态 `SUCCESS`。见 `matrix-gwt.md` G-03 | 状态为 `PAID`；DDL 枚举 `PENDING / PAID / CLOSED / REFUNDING / REFUNDED / PARTIAL_REFUNDED` 无 `SUCCESS` | PRD 用叙述性状态名，DDL 无此值；用例按 DDL 断言并输出 DEVIATION 日志 | 按 PRD 原文对接的消费方读不到 `SUCCESS`；文档与库表不一致 | 是（PRD G-03 改为 `PAID`；见 `errata.md` ERR-03） |
| D-09 | G-07：开票申请单初始状态 `PENDING`。见 `matrix-gwt.md` G-07 | `t_invoice_apply.state` 初始值 `APPLIED`，审批后 `ISSUED` | DDL 状态机为 `APPLIED → APPROVED / REJECTED → ISSUED`；PRD 的 `PENDING` 与审核中心五类单据的 `PENDING` 混用 | 按 PRD 查询 `PENDING` 发票申请返回 0 行 | 是（PRD G-07 改为 `APPLIED`；见 `errata.md` ERR-04） |
| D-10 | F16-1 / G-05：调价后历史行 `price_rule_id` 指向旧版本行，新请求指向新版本行。见 `matrix-82.md` F16-1、`matrix-gwt.md` G-05 | 调价在原 `t_price_rule` 行上 `version+1` 并写审计前后值，不生成独立旧版本行；历史 `consumed_micro` 与 `price_rule_id` 不变 | T15 定价模型用单行版本号 + 审计留痕，避免价格行复制 | 历史行指向的 `price_rule_id` 当前值已是新价，无法仅凭该行还原成交时单价；对账依赖 `t_usage_record` 金额快照 | 是（PRD/TDS 的版本行描述需改为单行版本 + 审计口径） |
| D-11 | G-05：同 D-10。见 `matrix-gwt.md` G-05 | 同 D-10 | 同 D-10 | 同 D-10 | 是（同 D-10） |
| D-12 | G-02：`est = raw_est × 1.2 = 7,200`（`raw_est=6,000`）。见 `matrix-gwt.md` G-02 | 流水线 `est = 6000 × 1.1 × 1.2 = 7,920`；`billing.prestimate_safety_bp=11000` 为安全系数；准入仍按 `raw_est` 判定，预扣被 `min(est, 可用×50%)` 截断 | PRD D-2 公式漏写安全系数；TDS §3.7 键表与 T07 实现均含 1.1 | 准入行为不变；预扣上限在可用余额充足时比 PRD 算式高 10%，文档与实现数值不一致 | 是（PRD D-2 / G-02 补 `× preestimate_safety_bp`；见 `errata.md` ERR-06） |
| D-13 | F1-1：请求向量模型返回 **404** `model_not_found`。见 `matrix-82.md` F1-1 | 返回 **400** `model_not_found`（`internal/gw/p07_model.go:55`、`apierr/codes.go`）；无向量模型专项用例 | PRD E.1 第 1823 行与 TDS 均定义 400；D.3 的 404 是孤立写法 | 客户端按 404 处理会漏判；错误码状态以 E.1 为准 | 是（PRD D.3 的 404 改为 400；见 `errata.md` ERR-02） |
| D-14 | F2-2：一张表含 RPM / TPM / 并发 / 日额度 / **积分** / 价格六项。见 `matrix-82.md` F2-2 | `PlanLimitTable` 表头为套餐 / RPM / TPM / 并发 / 日额度 / 价格；赠送积分只在 `PlanCards` 卡片展示 | 对比表按「限流规格」组织，积分与价格分属购买卡片 | 用户无法在同一张表比较各档赠送积分 | 否（属实现缺口） |
| D-15 | F6-2：四步向导可前进**后退**。见 `matrix-82.md` F6-2 | `goBack`（`CreateKeyDialog.tsx:138`）无自动化断言；仅前进路径与第 4 步越界被覆盖 | 用例按主路径编写，回退分支漏测 | 回退后字段保留/清空行为无回归保护 | 否（属测试缺口） |
| D-16 | F8-1：后台改上下文长度后模型广场显示新值。见 `matrix-82.md` F8-1 | 后端 `TestCatalogUsesStoredContextLength` 通过；`ModelsPage.tsx:137` 的上下文列无前端断言 | 页面渲染用例未覆盖该列 | 后端改值正确但前端可能渲染旧字段 | 否（属测试缺口） |
| D-17 | F8-3：在售模型标注流式 / 工具调用，且不显示缓存价。见 `matrix-82.md` F8-3 | 隐藏 `CATALOG_ONLY` 与不显示缓存价有用例；`ModelsPage.tsx:160` 的流式 / 工具调用徽标无断言 | 徽标按模型协议字段条件渲染，未写断言 | 徽标回归不可发现 | 否（属测试缺口） |
| D-18 | F22-4：模型列表可按来源地 / 类型 / 上架状态筛选，`仅目录` 行单独标色。见 `matrix-82.md` F22-4 | 后端三筛选有用例；前端筛选控件与 `warning` 徽标（`ModelsPage.tsx:152`、`:649`）无自动化断言 | 前端用例未覆盖后台模型列表 | 筛选参数或配色回归不可发现 | 否（属测试缺口） |
| D-19 | F22-6：推荐权重 0→10 保存成功，审计含前后值与操作人，首页位次前移。见 `matrix-82.md` F22-6 | 落库 0→10 有断言；首页位次仅由 F1-7 排序用例间接覆盖；`SetRecommendWeight` 审计前后值与操作人无专项用例 | 审计断言未单独编写 | 权重审计字段缺失时无法定位操作人 | 否（属测试缺口） |
| D-20 | F16-3：官网价格表与控制台广场均显示新价。见 `matrix-82.md` F16-3 | 控制台目录、K19、公开接口三处同价有断言；官网 `/pricing` DOM 未验，G-12 日志注明留待 Playwright | 官网是 SPA，Go 用例无法验 DOM | 官网价格表渲染回归不可发现 | 否（属测试缺口；补 Playwright） |
| D-21 | G-12：官网 `/pricing`、控制台广场、价格接口三处同一价。见 `matrix-gwt.md` G-12 | 控制台目录与 K19 一致；`/pricing` DOM 比对留待 Playwright 段 | 同 D-20 | 同 D-20 | 否（属测试缺口；补 Playwright） |
| D-22 | 65 码断言：`key_disabled` 须有自动化触发用例。见 `matrix-codes.md` key_disabled | 生产者 `internal/gw/p05_auth.go` 存在，无 `Test*` 用例 | 鉴权分支只做了代码路径覆盖 | 403 文案或状态码回归不可发现 | 否（属测试缺口） |
| D-23 | 65 码断言：`account_frozen_due_to_overdraft` 须有自动化触发用例。见 `matrix-codes.md` account_frozen_due_to_overdraft | 生产者 `internal/apierr/quota402.go` 存在，无 `Test*` 用例 | 透支冻结路径由 G-02 间接覆盖，未断言该码 | 402 码选择回归不可发现 | 否（属测试缺口） |
| D-24 | 65 码断言：`idempotent_replay_unsupported` 须有自动化触发用例。见 `matrix-codes.md` idempotent_replay_unsupported | 生产者 `internal/gw/server.go`（幂等重放正文 >64KB）存在，无 `Test*` 用例 | 大正文重放分支未构造 | 409 分支回归不可发现 | 否（属测试缺口） |
| D-25 | 65 码断言：`upstream_tls_failure` 须有自动化触发用例。见 `matrix-codes.md` upstream_tls_failure | 生产者 `internal/dispatch/dispatch.go` 存在，无 `Test*` 用例 | TLS 故障需真实证书环境，未做假上游 | 502 映射回归不可发现 | 否（属测试缺口） |
| D-26 | 65 码断言：`upstream_auth_failed` 须有自动化触发用例。见 `matrix-codes.md` upstream_auth_failed | 生产者 `internal/dispatch/dispatch.go` 存在，无 `Test*` 用例 | 上游 401/403 映射未构造 | 502 映射回归不可发现 | 否（属测试缺口） |
| D-27 | 65 码断言：`upstream_model_not_found` 须有自动化触发用例。见 `matrix-codes.md` upstream_model_not_found | 生产者 `internal/dispatch/dispatch.go` 存在，无 `Test*` 用例 | 上游模型不存在映射未构造 | 502 映射回归不可发现 | 否（属测试缺口） |
| D-28 | 65 码断言：`client_aborted` 须有自动化触发用例。见 `matrix-codes.md` client_aborted | 生产者 `internal/dispatch/dispatch.go` 存在，无 `Test*` 用例 | 客户端断连路径未构造 | 499 分流回归不可发现 | 否（属测试缺口） |
| D-29 | 65 码断言：`convert_failed` 须有自动化触发用例。见 `matrix-codes.md` convert_failed | 生产者 `internal/dispatch/dispatch.go` 存在，无 `Test*` 用例 | 协议转换失败分支未构造 | 500 映射回归不可发现 | 否（属测试缺口） |
| D-30 | 65 码断言：`auth_unavailable` 须有自动化触发用例。见 `matrix-codes.md` auth_unavailable | 生产者 `internal/control/auth/login.go` 存在，无 `Test*` 用例 | 会话/密钥后端不可用分支未构造 | 503 映射回归不可发现 | 否（属测试缺口） |

## 与 Go/No-Go 门槛的关系

| 门槛项 | 本表贡献 | 结论 |
| :--- | :--- | :--- |
| 82 项 `❌ == 0` | 本表不含 `❌`；F14-6 已于 T32（`68c` 系列提交）关闭，仅剩 F4-3 见 `open-issues.md` OI-01 | 仍不达标（`❌ 1`） |
| 82 项 `⚠️ ≤ 8` | 本表登记 14 条 matrix-82 `⚠️`：D-01、D-02、D-03、D-04、D-07、D-10、D-13、D-14、D-15–D-20 | 不达标（14 > 8） |
| GWT 全 `✅` | 本表登记 6 条 GWT `⚠️`（D-06 G-09、D-08 G-03、D-09 G-07、D-11 G-05、D-12 G-02、D-21 G-12）；D-05（G-06）已在 T31 关闭 | 不达标（**6/12 ✅**） |
| 65 码断言通过 | 本表登记 9 条码 `⚠️`（D-22–D-30） | 不达标（另有 7 条 `❌` 见 OI-06） |

## T34 关闭清单（2026-10-29）

| 编号 | 原偏差 | 关闭方式 | 证据 |
| :--- | :--- | :--- | :--- |
| D-01（F2-1） | 充值页不读 `plan`，落地后总是预选首档 | 充值页读 `?plan=`，按接口 `code` 匹配；不可购/非法值回落首档并提示 | `RechargePage.test.tsx` 3 条用例；反证关掉匹配分支即红 |
| D-02（F6-2） | 创建 Key 向导的「上一步」无测试 | 补四步回退用例，逐步断言已填值保留 | `CreateKeyDialog.test.tsx` |
| D-03（F8-1） | 广场「上下文」列无前端断言 | 补 `32,768 tokens` 断言 | `console/ModelsPage.test.tsx` |
| D-04（F8-3） | 流式/工具徽标无断言 | 四行夹具逐行断言两种徽标与配色 | 同上 |
| D-07（F20-4） | 提额**通过**不发站内通知 | `ApproveQuotaApply` 成功生效后发 `QUOTA_AUDIT`（事务外，失败不回滚） | `TestQuotaApproveNotification`；反证删 notify 即红 |
| D-10（F22-4） | 后台模型列表筛选/标色无前端断言 | 三筛选 query + `仅目录可见` 独立配色 | `admin/ModelsPage.test.tsx` |
| D-13（F22-6） | 推荐权重审计只写不验 | 断言 `operator_id` 与 before/after 含 0→10 | `TestRecommendWeightWritesAudit` |

> 未关闭的 7 条：F1-1（文档口径，待回写 PRD）、F2-2（缺「积分」列，产品口径）、F4-2（导入端点按 D-30 不实现）、F10-3（`CODE_USED` 原因 + `REDEEM_BATCH` 审批）、F16-1（价格版本行）、F16-3（`/pricing` DOM 待 Playwright 段）、F16-5（套餐分支无写入路径，OI-13）。
