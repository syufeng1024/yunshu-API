# T12 · 用户 / 密钥 / 模型域 API

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T12 |
| 阶段 | 4 · 控制面 |
| 优先级 | P0 |
| 依赖 | T11（+ T03 表结构、T06 `KeyAuthWriter`、**T10** 错误码） |
| 可并行 | 与 T13 / T14 / T15 并行 |
| 交付根 | `internal/control/user/`、`internal/control/key/`、`internal/control/model/` |
| 状态 | ✅ 通过（2026-10-27）· P0 必修已关闭 |

## 1. 目标

实现用户控制台最核心的三块 API：账户与会话内自助（概览、额度预测）、密钥全生命周期（含免费档 3 Key 限制、批量创建、分组只读）、模型广场（含推荐排序与三态过滤）。这三块是 F5–F8 共 20 个功能点的后端。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| PRD §5.1 | 519–538 | 概览：额度、触顶预测 `etaFor()`、请求曲线、提额入口 |
| PRD §5.2 | 538–562 | 密钥列表：搜索/筛选、新建四步、批量创建、停用二次确认 |
| PRD §5.3 | 562–582 | 密钥详情：**只显示前 8 位**、分组只读、限流实时刷新 5–10s、操作日志 |
| PRD §5.4 | 582–598 | 模型广场：单价与人民币参考、推荐权重排序、筛选、不显示缓存价 |
| PRD §3.2 / §3.2.1 | 243–326 | 免费档 3 Key、套餐取高不叠加、日额度 |
| PRD §3.4 / §3.5 | 364–417 | 模型三态、别名、分组只读、**后端不得接受 `group_code` 入参** |
| PRD D.3 | 1685–1701 | F5-2/F6-1–F6-7/F7-1–F7-4/F8-1–F8-4 的验收路径（**逐条落测试**） |
| TDS §1.3 / §1.4 | 1195–2023 | `t_api_key` 及授权范围表、`t_model` |
| TDS §3.2 P05 | 3293–3317 | K13 字段与写穿时机 |

## 3. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `internal/control/user/overview.go` | `GET /api/console/overview`：余额（积分 + 人民币）、今日用量、24h 曲线、套餐与四维额度、触顶预测 |
| 2 | `internal/control/user/overview.go` | 触顶预测：按近 1h 消耗速度外推到日额度上限的时刻；无套餐（无日额度）时返回 `null` 并说明 |
| 3 | `internal/control/key/list.go` | `GET /api/console/keys`：名称搜索 + 状态筛选（交集）、分页、掩码展示 |
| 4 | `internal/control/key/create.go` | `POST /api/console/keys`：四步参数（名称 / 模型范围 / 有效期 / IP 白名单）；**无「选分组」**；免费档活跃 Key ≥ 3 → 拒绝 |
| 5 | `internal/control/key/create.go` | 批量创建：`count`（1–20）；结果一次性返回全部明文（**仅此一次**） |
| 6 | `internal/control/key/detail.go` | `GET /api/console/keys/{id}`：只返回 `sk-` + 前 8 位 + 掩码；不属于本人 → 403/不存在 |
| 7 | `internal/control/key/update.go` | `PATCH /api/console/keys/{id}`：名称、模型范围、有效期、IP 白名单、启停；**拒绝 `group_code` 入参 → 400/403** |
| 8 | `internal/control/key/disable.go` | 启停：需二次确认语义（前端传 `confirm=true`），服务端幂等 |
| 9 | `internal/control/key/audit.go` | `GET /api/console/keys/{id}/audit`：操作日志含前后值 |
| 10 | `internal/control/key/rotation.go` | `POST /api/console/keys/{id}/rotate`：重新生成，旧 Key 立即失效（或宽限 5 分钟，择一并在文档写明） |
| 11 | `internal/control/model/catalog.go` | `GET /api/console/models`：`LISTED` only；按 `(recommend_weight DESC, published_at DESC)` 排序；按类型/厂商筛选；单价 + 人民币参考（1 元 = 100 积分） |
| 12 | `internal/control/model/catalog.go` | **不显示缓存价**（`pricing.cache_billing_enabled=false` 时）；标注流式/工具调用能力 |
| 13 | `internal/control/key/push.go` | 写穿 K13：创建/启用/停用/改名/删除/改分组时同步 Redis（调 T06 `KeyAuthWriter`） |
| 14 | `internal/control/key/*_test.go` | F6-1..F6-7、F7-1..F7-4 全覆盖；含 `group_code` 注入用例 |
| 15 | `internal/control/model/*_test.go` | F8-1..F8-4；含 `CATALOG_ONLY` 不出现 |
| 16 | `api/openapi.yaml`（追加） | 本卡接口的 request/response schema |

## 4. 硬约束（违反即驳回）

1. **完整 Key 只返回一次**：创建/轮换响应返回明文；此后任何读取接口只返回 `前 8 位 + 掩码`。`t_api_key` 只存 `key_hash`，**不存明文**。
2. **免费档 3 Key**：`FREE_DEFAULT` 账户活跃 Key（未停用且未过期）≥ `quota.free_max_active_keys`（3）→ 创建被拒，提示「免费档最多 3 个 Key，升级套餐可扩容」。付费三档不设数量上限。
3. **`group_code` 后端必须拒绝**：任何控制台写接口携带 `group_code` → 400 `invalid_request_body` 或 403 `permission_denied`。**前端隐藏不等于后端安全**（PRD F7-4）。
4. **分组只读**：密钥详情返回 `group_code` 与倍率，但**无升档/降档接口**（`pricing.group_switch_mode=ops_only`）。
5. **模型广场只出 `LISTED`**：`CATALOG_ONLY` 与 `DELISTED` 一律不返回；`region=OVERSEAS` 不返回。
6. **排序键固定**：`(recommend_weight DESC, published_at DESC)`。筛选后**不得**退回录入序。
7. **价格只读库**：单价从 `t_price_rule` 读，**不得**乘入分组倍率（广场展示标准价，PRD F1-5：只显示 `standard` 单价）。
8. **模型授权范围**：`model_scope_mode=SELECTED` 时只允许 `t_api_key_model` 内模型；校验在网关（T07 P07）与控制面双处一致。
9. **`PATCH` 语义**：只更新传入字段；空数组/空串需与"不传"区分（用指针类型），避免把白名单误清空。
10. **限流实时值**（详情页 5–10s 刷新）：返回 Redis 当前值 + 1s 内缓存；Redis 不可用时返回 `null` 而非 0（不能显示假的"未使用"）。
11. **越权不泄露存在性**：访问他人 Key 返回 403 或「密钥不存在」，**不区分**。
12. **体验积分领取是主动动作**：不得在注册时自动发放（`trial_claimed_at` 为空表示未领）。
13. **审计**：密钥的创建/停用/启用/改权限/轮换/删除必留前后值（PRD F7-3）。

## 5. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `go test ./internal/control/key/... ./internal/control/model/... -race` 全绿 | 复跑 |
| A2 | F6-3：创建后再次读取 → 只见前 8 位；DB 中无明文 | 单测 + SQL |
| A3 | F6-7：免费档已有 3 活跃 Key → 创建 400，停用 1 个后可建 | 单测 |
| A4 | F7-1：B 访问 A 的 Key → 403 或不存在（不泄露） | 单测 |
| A5 | F7-4：`PATCH` 带 `group_code=internal` → 400/403，分组未变 | 单测 |
| A6 | F6-2：创建流程参数无分组字段；缺失必填项 → 400 指明字段 | 单测 |
| A7 | F6-5：批量 10 → 生成 10 个 Key，响应含 10 个明文，DB 中 10 条 | 单测 |
| A8 | F8-4：M1 权重 100 / M2 权重 0 → 列表 M1 在前；筛「文本」后顺序不变 | 单测 |
| A9 | F8-3：`CATALOG_ONLY` 不出现；关闭缓存计费时不返回缓存价 | 单测 |
| A10 | F8-2：输入 1500 微积分/千 token → 显示 0.15 积分 + 0.0015 元参考 | 单测 |
| A11 | 触顶预测：日额度 5000、近 1h 耗 500 → 预测约 10h 后触顶（±10%） | 单测 |
| A12 | 写穿：停用 Key 后 Redis K13 中 `st != ENABLED` | 集成测试 |
| A13 | 审计：改权限后 `t_api_key_audit` 有前后值 | 单测 |
| A14 | 分页上限：`page_size=1000` → 400 或截断为 100 | 单测 |

## 6. 自检命令

```bash
go test ./internal/control/key/... ./internal/control/model/... -race -cover
curl -s localhost:8081/api/console/keys -H "Cookie: sid=$SID" | jq '.items[0]'
docker exec -i $(docker compose -f deploy/docker-compose.yml ps -q mysql) mysql -uroot -proot yunshu \
  -N -e "SELECT key_hash, prefix FROM t_api_key LIMIT 1"   # 确认无明文列
```

## 7. 禁止事项

- 不得存 Key 明文或可逆加密的 Key。
- 不得提供用户侧改分组的接口。
- 不得把 `CATALOG_ONLY` 模型混入广场（列出来又调不通，比不列更伤）。
- 不得在广场展示合约价 / 内部价。
- 不得让"批量创建"绕过免费档 3 Key 限制。

## 8. 完成回执

```
卡号: T12
接口清单: 路径 / 方法 / 权限点 / 对应 F 编号
F 覆盖: F5-2,F5-3,F6-1..F6-7,F7-1..F7-4,F8-1..F8-4 逐条 → 用例名
Key 明文策略: 存储列 + 返回时机（贴 SQL 证据）
免费档限制: A3 输出
group_code 拒绝: A5 输出
排序: A8 输出
遗留:
```

---

## 9. 完成回执与验收记录（2026-10-27）

**交付**：277 轮 / 2204s / $29.43。新增 `internal/control/{user,key,model}` + 三个辅助包
（`plan` / `units` / `ctltest`）；改 `internal/apierr/{codes,codes_test}`、`internal/control/{server,mw/*,auth/*}`、
`cmd/controld/main.go`、`api/openapi.yaml`，重生成 `docs/error-codes.md`。未 commit。

**验收方式**：交付总监独立复跑。清空开发库与 Redis、从空库重来、自起四进程。

| 项 | 结果 |
| :--- | :--- |
| `go build` / `go vet` / `gofmt`(交付路径) / `lint.sh`(8 条) | 全过 |
| `go test ./internal/control/... ./internal/apikey/... ./internal/apierr/...` | 全绿 |
| **E-21 码表** | `TestCodeCountIs70` 通过；`docs/error-codes.md` 70 数据行；新码已在线上生效（`page_size=1000` → 400 `invalid_query_parameter`，未登录 → 401 `session_expired`） |
| A2 脱敏 | 详情无 `key` 字段，只有 `key_masked`；`t_api_key` 明文列扫描 **0** |
| A3 免费档 | 1 → 建到 3（201）→ 第 4 个 **409 `key_limit_reached`**（首轮为 400，见下方必修项，已修复）；停用 1 个后（活跃 2）再建 **201** |
| A4 越权 | 他人 GET/PATCH/audit/rotate/delete key 1 → 全部 **404 `key_not_found`**，key 未被改动 |
| A5 group_code | 创建/PATCH 带 `group_code` → **400**，DB 仍 `standard` |
| A8 排序 | 造权重 deepseek=100 / qwen=50 → 顺序正确；加 `model_type=TEXT` 筛选后顺序不变 |
| A10 价格 | 2000 微积分/千 → `0.2` 积分 / `0.002` 元（1 积分=10000 微积分，1 元=100 积分） |
| A12 K13 写穿 | 停用后 `ak:*` 出现 `st=DISABLED` |
| A13 审计 | 动作覆盖 CREATE 5 / DISABLE 1 / MODEL_SCOPE_CHANGE 1 / DELETE 1 / ROTATE 1，且均带前后 JSON |
| A14 分页 | `page_size=1000` → 400 `invalid_query_parameter` |
| 软删 | `key_name` 变 `xxx (已删除)`，`deleted_at` 置位，活跃计数口径 `deleted_at IS NULL` 使槽位释放（3 活跃 + 6 行 = 槽位确实释放） |
| 轮换 | 旧 Key `DISABLED`、新 Key `ENABLED`、明文只返回一次 |
| 回归 B1–B6 | seedctl 3 表各 1；`cfg:snap:ROUTING` 含 keyModels；非流式 200 + `X-Credits-Consumed: 116`；usage/ledger（PREHOLD→RELEASE→SETTLE）落库、余额 100000→99748；流式有 `X-Credits-Preheld` 无 `X-Credits-Consumed`；无 Key/错 Key 401、越权模型 403 |

### 必修 1 项（P0，派 T13 前完成）—— ✅ 已关闭（2026-10-27）

> **关闭说明**：本轮修复已由工程师交付、QA 三层验证、交付总监独立复跑后通过。证据见本节末尾「修复交付与验证」。以下保留原始问题描述，不删改，以便追溯「为什么会有第 70 个码」。

**免费档超限与状态类拒绝复用了 `invalid_request_body`，文案是错的。**

实测：
```
第 4 个 Key → 400 {"code":"invalid_request_body",
                  "message":"请求体不是合法 JSON：count",
                  "hint":"按 message 中的字段路径修正请求体"}
对已停用 Key 轮换 → 同一文案，字段路径 "count"
```

问题有两层：
1. 违反本卡**硬约束 2** 明文要求——拒绝时须提示「免费档最多 3 个 Key，升级套餐可扩容」，当前没有这句话。
2. 请求体本身完全合法，却被告知「不是合法 JSON」。用户按 hint 去改 `count` 字段永远改不好，这是误导性诊断，比不给提示更差。

根因是 `mw.InvalidField()` 被当成了通用拒绝出口。修正方向：为「配额/状态不足」增加专用错误码
（实现方建议 `key_limit_reached` 409，可采纳）与不带「JSON」字样的 message/hint；`confirm` 缺失这类真·body 问题才走 `invalid_request_body`。

**A7（批量 10）与 A11（触顶预测）未做线上验证**：免费档上限 3 挡住批量、FREE_DEFAULT 无日额度使 ETA 返回
`NO_DAILY_QUOTA`（这本身是对的）。两项只有单测证据，记录在案。

#### 修复交付与验证（2026-10-27）

**实现**：`checkFreeLimit` 出口由 `mw.InvalidField("count")` 改为 `apierr.Newf(apierr.CodeKeyLimitReached, limit, active, adding)`；新增第 70 码 `key_limit_reached`（409 / `conflict_error` / 不可重试 / `BUSINESS`），上限值取自 `quota.free_max_active_keys` 运行时配置（`service.go:201` 的 `freeMaxActiveKeys()`），文案内不出现字面量 3；`mw.InvalidField` 的文档收窄为「body 形状专用」并写明状态类拒绝的正确去向，防复发。

**改动 8 个文件**：`internal/apierr/{codes,codes_test}.go`、`internal/control/key/{service,key_test,main_test}.go`、`internal/control/mw/errs.go`、`docs/error-codes.md`（`MarkdownTable()` 重生成）、`api/openapi.yaml`（`POST /keys` 与 `POST /keys/{id}/rotate` 各补 409，400 描述收窄）。

**三层验证**：

| 层 | 内容 | 结果 |
| :--- | :--- | :--- |
| 静态与单测 | `go build` / `go vet` / `go test -count=1 ./internal/control/... ./internal/apierr/... ./internal/apikey/...` / `lint.sh` 8 条 / `TestCodeCountIs70` 等 4 条码表用例 | 全绿 |
| 断言质量 | `TestFreeTierThreeActiveKeys` 断言 409 + code + type + message 含「免费档最多 3 个 Key」+ message/hint 均不含 JSON（6 项，非只看状态码） | 通过 |
| **反向验证** | 把出口临时改回 `mw.InvalidField("count")` → 3 条用例变红（`want 409, got 400`）、`TestBodyErrorsStayInvalidRequestBody` 仍绿 → 证明断言有判别力且两组出口确实分离；改回后 SHA256 与改动前逐字节一致（`52548e3c…`） | 通过 |
| 配置驱动 | 库里把 `quota.free_max_active_keys` 3→5 → 可建到 5 个、第 6 个 409 且 message 变「最多 5 个」 → 还原 3 | 通过 |
| 真实 HTTP E2E | 独立库 `yunshu_e2e` + 真实 `controld:8081`：建 3（201×3）→ 第 4 个 **409** 全文 `{"type":"conflict_error","code":"key_limit_reached","message":"免费档最多 3 个 Key，升级套餐可扩容（当前已启用 3 个，本次需新增 1 个）","retry_after_seconds":0,"hint":"停用一个已有 Key 释放名额后重试；付费套餐不限 Key 数量，可在控制台 → 套餐页升级"}` → 停用 1 个后 **201** → 轮换已停用 Key **409** → `count=11` 仍 **400 `invalid_request_body`** | 通过 |
| 拒后副作用 | 被拒的创建/轮换均未残留行（3→3、4→4）；`t_api_key` 无明文列；越权仍 404 `key_not_found`（不因新码泄露存在性） | 通过 |

**遗留（不影响通过）**：`TestFreeTierThreeActiveKeys` 里的「3」是测试固化预期值，依赖测试库配置行；防硬编码由 `TestKeyLimitReachedIsStateConflict`（Newf 传 5 验 5）与配置 3→5 实证保证。若后续其它接口也会撞限额，需按同一模式补 409 与用例。

### 实现方上报的偏离（12 条，均已复核成立）

| # | 内容 | 处置 |
| :--- | :--- | :--- |
| 1 | 批量 `count` 卡写 1–20，PRD 写 1–10 | **按 PRD 取 1–10**，卡待改 |
| 2 | PRD 要求用户不可自选「永不过期」，T12 卡未写；实现为创建必须给未来 `expire_at` | 采纳，卡待补 |
| 3 | 免费档拒绝无专用码 | ✅ **已修**：第 70 码 `key_limit_reached`（409），见上 |
| 4 | 审计新增 `RENAME` / `EXPIRE_CHANGE` 两个动作值 | TDS 注释待回写 |
| 5 | TDS 软删 `CONCAT(key_name,' (已删除)')` 会撑破 `varchar(64)`，实现截断到 58 字符 | 采纳，TDS 待回写 |
| 6 | `POST /api/console/keys` 响应形状改为 `{items,count,warning}` | T17/T18 必须按新形状生成类型 |
| 7 | OpenAPI `sessionCookie.name` 原写 `yunshu_session`，实际是 `sid` | 已修正 |
| 8 | `gofmt -l` 列出的 11 个 apierr 文件是 CRLF 检出遗留 | 属实，非本卡引入 |
| 9 | IP 白名单与 ROUTING 快照有最长 4 分钟传播缺口，已在 `Deps` 加 `PublishRoutes` 回调、先发快照再写 K13 | **采纳，这条不做就是越权级缺口** |
| 10 | 免费档 `used_today_micro` 显示 0 但 `consumed_micro` 有真实值 | 前端按「无上限」渲染 |
| 11 | ROUTING 快照保留已软删 Key 的 IP 规则 | 待 T15/T11 重构发布器时过滤 |
| 12 | A1 未跑 `-race`（本机无 gcc） | 留 T21 Linux CI |

**遗留**：T13 充值/订单/体验积分领取；T14 用量账单；T15 Key 分组升降、永久 Key、配置发布端点；T17/T18 按新响应形状生成类型；T21 `-race` 与 CVE 审计。（原列的「`key_limit_reached` 码」已在本轮 P0 修复中落地，不再遗留）
