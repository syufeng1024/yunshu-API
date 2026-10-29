# 错误码逐码裁定书（T39 · 7 个无生产者码）

> 依据：PRD V1.2 附录 E.1、`internal/apierr/codes.go`、errata **ERR-05**（以代码 76 为唯一可枚举事实）。
> 裁定人：交付总监。日期：2026-10-29。
> 结论口径（三选一）：**A 补生产者**（本卡就做）/ **B 需单独立项**（PRD 有明确功能要求，缺口是功能不是码表）/ **C 登记不实现**（PRD 自己说不做，或实现里没有检测面，且补约束会破坏既有语义）。

## 一、总表

| 码 | HTTP | PRD 语义（行号） | 代码现状 | 裁定 | 归属 |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `upstream_key_unavailable` | 503 | 上游凭据服务不可用，**全部候选解密失败**（`:1915`） | 解密失败走 `config_unavailable`（`internal/dispatch/dispatch.go:433`）——**码用错了**，不是没有生产者 | **A 补生产者** | T39 段 1 |
| `account_frozen` | 403 | 注销后 **7 天冷静期**内调用暂停（`:1231`） | 注销即置 `DISABLED`（`internal/control/auth/cancel.go:20`）；`t_user.status` 只有 `NORMAL / DISABLED / FROZEN_OVERDRAFT`（`0001_account_credential.sql:14`），**没有冷静期状态** | **C 登记不实现**（缺口是「冷静期功能未实现」，记 D-31） | deviations |
| `duplicate_request_in_flight` | 409 | 未带幂等键时 **10 秒内完全相同请求**自动去重；非流式回 200 缓存体，**流式回 409**（`:1897`、`:1993`、`:2007`） | `idemstore.Options.DuplicateWindow` 已声明（`internal/idemstore/idem.go:95`），但**全仓没有任何读取点**，只在 `DefaultOptions` 里被赋值（`:103`）——去重未接线 | **B 需单独立项**（`idem.duplicate_window_seconds` 是 PRD 默认开启的功能，补码等于补功能） | 新卡 T41 |
| `duplicate_request_id` | 409 | `X-Request-Id` 被复用（`:1898`） | `t_idempotency_record` 唯一键是 `uk_idem (scope, idem_key)`（`0003_fund_usage_ops.sql:135`），**`request_id` 无唯一约束**，没有检测面 | **C 登记不实现**（加唯一键会与「同键重放」「traceparent 透传同一个 trace-id」冲突；记 D-32） | deviations |
| `config_item_not_found` | 404 | 配置项不存在，`hint` 区分「全局未配置 / 该作用域未覆盖」（`:1942`） | 管理端只有 `GET /api/admin/config`（恒返回全量 45 键，`internal/control/ops/config.go:52`）与 `PUT`，**没有单键读取端点**，不存在「查不到」的路径 | **C 登记不实现**（无对应端点；将来加单键端点再启用，记 D-33） | deviations |
| `account_rpm_exceeded` | 429 | **V1.0 不实现**，账户级 RPM 是第 5 维，保留登记（`:1949`） | 无 | **C 登记不实现**（PRD 已裁定，非缺陷） | matrix 标注 |
| `content_policy_violation` | 403 | 平台侧入口拦截，命中即 403、不计费不预扣（`:1208`、F-7 `:1620`、E.1.11） | 无钩子；`t_content_policy_hit` 只有建表与测试插入（OI-03） | **B 需单独立项**（拦截依赖规则源/供应商，属功能+合规决策，不能靠「空钩子」凑码） | OI-03 保留 |

**结果**：A 类 1 个（本卡修）；B 类 2 个（立项，码表侧不阻塞文档收口）；C 类 4 个（登记不实现 + 偏差登记）。

## 二、逐条裁定依据

### 1. `upstream_key_unavailable`（A · 补生产者）

- PRD `:1915` 定义范围极窄：「**全部候选解密失败**（KMS / 凭据配置）」，可重试 60s，责任方**平台**。
- 实现里这个条件确实会发生：`dispatch.go:427` 的 `decryptKey` 失败（KEK 未配、信封损坏、`v1:` 前缀错）走 `:433` 返回 `CodeConfigUnavailable`。
- `config_unavailable` 的语义是「路由/价格快照缺失」（`p07_model.go`、`dispatch.go:166` 的 `targets()` 分支），与「凭据解不开」不是一回事：前者让运维去看配置发布，后者让运维去看 KEK/凭据。混用会把**平台侧 5xx 的排障方向指错**。
- 裁定：把 `:433` 改成 `upstream_key_unavailable`；`buildBody` 失败（`:445`）**保持** `config_unavailable`（那是上游模型映射/请求构造的数据问题，仍属配置）。
- 边界：**不改** `:166`（快照缺失）与 `:445`（请求构造）；不改 `usagerecord.go:199` 的归因表（`config_unavailable` 仍归 `ROUTE/PLATFORM`），新增码归 `UPSTREAM/UPSTREAM` 还是 `ROUTE/PLATFORM` 需在卡里明确——按 PRD「责任方平台」，归 **`ROUTE / PLATFORM`**。

### 2. `account_frozen`（C · 登记不实现）

- PRD 把它绑定在「注销冷静期」上（`:1231`），不是运营冻结。运营侧的「禁用」对应 `account_disabled`（已有生产者）。
- 实现里注销是**立即生效**：`cancel.go:20` 写 `disableReason = "用户主动注销"`，账号转 `DISABLED`；没有 7 天窗口，也没有可撤销期。
- 因此该码没有合法生产者，**不是码表漏了，而是冷静期功能没做**。补生产者等于补一整套冷静期（新状态 + 到期 job + 撤销端点 + 冷静期内余额冻结语义），远超码表收口。
- 裁定：登记不实现；偏差记 **D-24**（注销即禁用，无 7 天冷静期）；是否补功能由需求方定。

### 3. `duplicate_request_in_flight`（B · 需单独立项）

- PRD 这是**默认开启**的功能（`idem.duplicate_window = 10s`，`D-3` 裁定），且有六态验收里的第 ⑤ 态（非流式 200 + `X-Idempotent-Replay: true`；流式 409）。
- 实现里 `DuplicateWindow` 是**死字段**：`grep -rn DuplicateWindow internal/` 只有声明与 `DefaultOptions` 赋值，没有使用点。也就是说「未带键的 10s 去重」整体没接线。
- 裁定：**不塞进码表卡**。补这个码必须同时做合成键（`sha256(user|key|model|body_hash|floor(now/10s))`）、窗口判定、非流式缓存重放、流式 409、`X-Idempotent-Replay` 头——是一个功能卡（T41），且会动到 P08/P26 结算路径，风险等级高于文档收口。

### 4. `duplicate_request_id`（C · 登记不实现）

- PRD `:1898` 的可重试次数是 0、责任方业务，语义是「客户端把 `X-Request-Id` 复用到另一个请求上」。
- 实现里 request_id 的来源是 `X-Request-Id → traceparent trace-id → UUIDv7`（`internal/gw/server.go:76-82` 注释与 `apierr.ResolveRequestID`），**服务端把它当追踪标识，不当唯一键**；`t_idempotency_record` 的唯一键只有 `(scope, idem_key)`。
- 要产出这个码，得给 `request_id` 加唯一约束。这会直接破坏三件事：① 同键重放（同一个 request_id 二次进入）；② 分布式追踪里同一个 trace-id 跨多个子请求；③ 幂等记录的租约接管（接管方沿用原 request_id）。代价远大于收益。
- 裁定：登记不实现；偏差记 **D-25**。

### 5. `config_item_not_found`（C · 登记不实现）

- PRD `:1942` 的语义是「按 key（+ 作用域）读配置，读不到时区分全局缺失与作用域未覆盖」。
- 管理端现状：`GET /api/admin/config` 恒返回全部 45 键的合并视图（`ops/config.go:52` 起，缺行时用 `DEFAULT` 兜底），`PUT` 只做写入校验。**没有「按 key 读单个」的端点**，所以不存在「查不到」的分支。
- 裁定：登记不实现；偏差记 **D-26**。若将来新增 `GET /api/admin/config/{key}`，再启用该码。

### 6. `account_rpm_exceeded`（C · PRD 已裁定）

- PRD `:1949` 原文写「**V1.0 不实现**：账户级 RPM 是第 5 维，已裁定不做，保留登记」，并注明「V1.0 不会出现该码」。
- 裁定：登记不实现，**不补假生产者**（造一个永不可达的分支只会让 65 码断言变绿而运行时行为不变）。

### 7. `content_policy_violation`（B · 需单独立项，OI-03 保留）

- PRD F-7（`:1620`）明确「V1.0 内」「E.1 64→65」「入口拦截命中即 403、不计费、不占预扣」，`§10.1`（`:1208`）给出完整语义。
- 实现无任何钩子：`t_content_policy_hit` 只有建表（迁移）与测试插入，网关 P01–P14 没有任何调用点。
- 裁定：这不是「补一行代码」能解决的——拦截需要规则源（关键词库 / 外部内容安全服务），而 V1.0 的配置键集合是**冻结的 45 个**（D-08），不能临时加 `policy.blocklist`。必须先由需求方定「用哪家/用什么规则」。定之前**不得**写一个永不触发的空钩子来凑码表。OI-03 保留，责任人仍为「网关 + 风控后端」，前置是需求方选型。

## 三、对门槛 3 的诚实结论

| 项 | 收口后状态 |
| :--- | :--- |
| 口径（OI-08 / ERR-05） | ✅ 本卡可关：PRD 65 基线 + 11 新增 == 76，TDS §6.2 勘误已在 T37 落行，T23 A4 改写 |
| 7 个无生产者码 | 1 个补生产者（`upstream_key_unavailable`），4 个登记不实现，**2 个功能缺口**（`content_policy_violation`、`duplicate_request_in_flight`） |
| 9 个无自动化用例的码（OI-07） | 本卡不动，另派段 2 |

**门槛 3 仍为 ❌**，阻塞项从「7 个码无生产者 + 口径不一致」收窄为「**2 个功能缺口 + 9 个缺用例**」。不得因为「4 个已登记不实现」就把门槛记成通过——`content_policy_violation` 是 PRD 明确要求 V1.0 实现的合规能力，`duplicate_request_in_flight` 是默认开启的防误双击功能。

## 四、给后续卡的输入

| 卡 | 内容 | 前置 |
| :--- | :--- | :--- |
| T39 段 1 | A 类补生产者 + 口径回写 + 裁定落盘 | 无 |
| T39 段 2 | OI-07 的 9 条自动化用例（假上游构造 TLS/401/模型不存在/断连；`auth_unavailable` 用可控依赖故障） | 段 1 |
| T41（建议） | `duplicate_request_in_flight`：接通 `DuplicateWindow`，非流式 200 重放 + 流式 409 + `X-Idempotent-Replay` | 需求方确认是否纳入 V1.0 |
| T42（建议） | `content_policy_violation`：先定规则源/供应商，再接入 P04 钩子 + `t_content_policy_hit` 落库 + 不计费断言 | 需求方选型 |
| 待定 | `account_frozen`：7 天冷静期（状态机 + 到期 job + 撤销端点） | 需求方确认是否纳入 V1.0 |
