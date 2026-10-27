# T15 · 运营后台 API（渠道/模型/定价/分组/公告/风控）

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T15 |
| 阶段 | 4 · 控制面 |
| 优先级 | P0 |
| 依赖 | T11、T05、**T10**（错误码） |
| 可并行 | 与 T12 / T13 / T14 并行 |
| 交付根 | `internal/control/ops/` |
| 状态 | 🟨 第一段已交付（2026-10-27）· 交付物 1–7 + 状态码收敛；第二段（定价/分组/公告/风控/用户/配置）未派 |

## 1. 目标

运营的六件事：配渠道、定价、分组、发公告、管用户、盯风控。本卡的核心不是 CRUD，而是**几条会直接造成资金或声誉损失的校验**：境外来源地拒绝保存、毛利地板、调价公示期、价格唯一真相源、渠道被引用不可删、上游 Key 密文存储。

## 2. 必读

| 文档 | 区间 | 要点 |
| :--- | :--- | :--- |
| PRD §6.1 | 752–763 | 运营概览 |
| PRD §6.2 | 763–787 | 渠道管理：F14-1–F14-6（含来源地拒绝、专属范围） |
| PRD §6.3 | 787–811 | 用户管理：搜索、禁用/启用、禁用原因不对外 |
| PRD §6.4 | 811–860 | 倍率与定价：F16-1–F16-7（公示期、地板、唯一真相源） |
| PRD §6.5 | 860–875 | 公告与风控 |
| PRD §3.4 / §3.5 | 364–417 | 来源地强制、模型三态、定价层级、倍率 `>0 且 ≤1`、地板公式 |
| PRD D.3 | 1716–1731 | F14 / F16 / F17 验收路径（逐条落测试） |
| TDS §1.4 | 1424–2023 | `t_channel` / `t_model` / `t_price_rule` / `t_price_group` / `t_config_*` |
| TDS §1.8 | 2698–2884 | 公告位掩码、风控规则与命中、审计 |
| TDS §3.7 | 434–517 | `/admin/config` 只接受 12 个白名单键 |

## 3. 交付物清单

| # | 路径 | 职责 |
| :--- | :--- | :--- |
| 1 | `internal/control/ops/channel.go` | 渠道 CRUD：列表/详情/新建/编辑/停用/删除；删除时若被路由引用 → **409 `channel_in_use`** |
| 2 | `internal/control/ops/channel.go` | **来源地校验**：选「境外」→ 拒绝保存（400 + 明确提示），境内可保存 |
| 3 | `internal/control/ops/channel.go` | 上游 Key：入库存密文（信封加密），出参掩码（只显末 4 位），**任何接口不得回吐明文** |
| 4 | `internal/control/ops/channel.go` | 权重变更即时生效 + 审计前后值；`group_scope_mode` 允许分组；`dedicated_scope_mode` + 专属用户名单维护 |
| 5 | `internal/control/ops/model.go` | 模型主数据管理：对外名、别名、类型、上下文、单价、缓存倍率、阶梯、协议、归属渠道、`listing_state`、`recommend_weight` |
| 6 | `internal/control/ops/model.go` | 三态切换：`LISTED` ↔ `CATALOG_ONLY` ↔ `DELISTED`；`LISTED→CATALOG_ONLY` 不需公示期，写审计 + **生成公告草稿** |
| 7 | `internal/control/ops/model.go` | 推荐权重：`CATALOG_ONLY` 模型设非 0 权重 → 拒绝；`LISTED` 改 `CATALOG_ONLY` 时权重自动失效 |
| 8 | `internal/control/ops/pricing.go` | 单价保存：负数/0 → 400 指出字段；**唯一真相源校验**（同一模型两处配价 → 拦截并指出两份来源） |
| 9 | `internal/control/ops/pricing.go` | **公示期**：涨价须提前 ≥ 7 天生效，否则拒绝；降价可即时生效 |
| 10 | `internal/control/ops/pricing.go` | **毛利地板**：`标准单价 × 倍率 ≥ 成本价 × 4/3`；缺成本价时降级为「警告 + 二次确认」，**不得静默上架**；`internal` 分组例外 |
| 11 | `internal/control/ops/group.go` | 分组管理：`standard 10000` / `contract 8500` / `internal 0`；倍率须 `>0 且 ≤1`（`internal` 的 0 为唯一例外）；范围（模型集 / 渠道集）维护 |
| 12 | `internal/control/ops/group.go` | 倍率变更 → 批量写穿该分组下全部 Key 的 K13 `gp` 字段（异步，允许短暂旧值） |
| 13 | `internal/control/ops/announcement.go` | 公告：发布/定时发布/草稿；「活动」类不得与调价公示冲突；模型上架自动生成待发布「上新」草稿 |
| 14 | `internal/control/ops/announcement.go` | 公告**不反向回滚**模型状态（PRD F17-3） |
| 15 | `internal/control/ops/risk.go` | 风控：规则 CRUD、命中记录查询、用户风险分调整（审计） |
| 16 | `internal/control/ops/users.go` | 用户管理：列表（邮箱/手机/用户号/状态筛选）、禁用/启用（`disable_source` = `OPS_MANUAL`）、余额与套餐查看 |
| 17 | `internal/control/ops/users.go` | 禁用原因**仅运营可见**，用户侧只看到「账号已被禁用，请联系客服」 |
| 18 | `internal/control/ops/config.go` | `GET/PUT /admin/config`：只接受 12 个白名单键；`quota.*` 四键只接受 V1 已实现值；其余一律 `config_value_invalid` |
| 19 | `internal/control/ops/*_test.go` | F14-1–F14-6、F16-1–F16-7、F17-1–F17-3 全覆盖 |
| 20 | `api/openapi.yaml`（追加） | 本卡接口 schema |

## 4. 硬约束（违反即驳回）

1. **来源地境外一律拒绝保存**：渠道表单与模型主数据选「境外」→ 400 拒绝。且即使被误配入库，路由层（T07）也不得选中——**双保险，缺一不可**。
2. **删除渠道前查引用**：被 `t_route` 引用 → 409 `channel_in_use`，`hint` 给出解除路径（先解引用或改为停用）。
3. **上游 Key 密文**：DB 存密文，接口出参掩码，审计日志不落明文。`grep` 检查序列化结构里不得含 `upstream_key` 明文字段。
4. **毛利地板公式精确**：`标准单价 × 分组倍率 ≥ 上游成本单价 × 4/3`，**唯一实现口径** = `成本 × 10000 ÷ 7500` 向上取整 + 整数交叉相乘比较。**禁止**写 `× 1.25` / `× 1.333` / `×(10000+bp)`。
5. **`internal` 分组（倍率 0）不参与地板校验**，改用 PRD §6.4 F16-5 四项约束兜底。
6. **调价公示期 7 天**：涨价且生效时间距今 < 7 天 → 拒绝；降价允许即时。判定以服务端时间为准，不信任前端。
7. **调价不回溯历史账单**：改价只影响生效时间之后的请求（PRD F16-1）。
8. **价格唯一真相源**：同一模型不允许两处配置不同单价；冲突时拦截并**指出两份来源**（表名 + 行 id）。
9. **分组倍率区间**：`>0 且 ≤1`（`internal` 的 `0` 是唯一例外，语义为「不计费」而非「免费打折」，`remark` 必须写明）。**不设 >1 的倍率**。
10. **`group_code` 只能由运营设置**：`contract` 分组必须经审批（走 T16 审核中心）；用户侧无入口。
11. **推荐权重约束**：非 0 仅对 `LISTED` 有效；`CATALOG_ONLY` 设权重 → 拒绝。
12. **公告不回滚业务状态**：公告只是通知载体，`F17-3` 明确要求状态与公告解耦。
13. **用户禁用原因不对外**：`account_disabled` 的错误体只给通用文案，**不透露类别**。
14. **配置白名单**：只 12 键可运行时写；写其它键 → 400 `config_value_invalid`；写 `quota.overdraft_warn_days` 同样拒绝。
15. **所有写操作审计含前后值**（T11 中间件）+ 涉及价格/倍率/上限的变更必须写 `t_config_publish` 或等价留痕。
16. **不得提供"一键清除缓存"的万能接口**：失效必须按 namespace / 按 key 精确失效。

## 5. 验收标准

| # | 判定 | 方式 |
| :--- | :--- | :--- |
| A1 | `go test ./internal/control/ops/... -race` 全绿 | 复跑 |
| A2 | F14-5：来源地「境外」→ 拒绝保存；「境内」→ 成功 | 单测 |
| A3 | F14-1：删除被引用渠道 → 409 `channel_in_use` + hint；访问已删渠道详情 → 友好提示无 500 | 单测 |
| A4 | F14-2：新建渠道含上游 Key → 列表掩码、DB 密文（贴 SQL 证据） | 单测 |
| A5 | F14-3：权重 100→10 → 生效且审计含前后值 | 单测 |
| A6 | F14-4：渠道只允许 `contract` → `standard` 用户请求不被路由 | 单测（配合 T07） |
| A7 | F14-6：专属渠道 → 非名单用户候选集不含；渠道停用后走降级且日志可见 | 单测（配合 T07） |
| A8 | F16-2：单价 0 或负数 → 拒绝并指出字段 | 单测 |
| A9 | F16-3：改输出价 → 官网价格表与控制台广场同步新价 | 单测 |
| A10 | F16-4：涨价且 3 天后生效 → 拒绝；降价即时 → 通过 | 单测 |
| A11 | F16-5：`std=1000,ratio=8500,cost=800` → 拒绝并展示差额；`cost=600` → 通过 | 单测（精确数值） |
| A12 | F16-6：同模型两处配价 → 拦截并列出两份来源 | 单测 |
| A13 | F16-7 / F17-1 / F17-3：`LISTED→CATALOG_ONLY` 生成草稿、无需公示期；`CATALOG_ONLY→LISTED` 生成上新草稿；公告未发布不回滚状态 | 单测 |
| A14 | F17-2：公告定时到点自动发布 | 单测（假时钟） |
| A15 | 配置：12 键写入成功、第 13 键与 `quota.overdraft_warn_days` 被拒 | 表驱动单测 |
| A16 | 倍率：`0.85` 存 `8500`；`1.2` → 拒绝；`internal` 为 0 例外 | 单测 |

## 6. 自检命令

```bash
go test ./internal/control/ops/... -race -cover
grep -rn "1.25\|1.333\|float64" internal/control/ops/pricing.go || echo "OK: 地板用整数"
M() { docker exec -i $(docker compose -f deploy/docker-compose.yml ps -q mysql) mysql -uroot -proot yunshu -N -e "$1"; }
M "SELECT id,name,region,upstream_key_enc IS NOT NULL FROM t_channel"
```

## 7. 禁止事项

- 不得为"运营方便"放开来源地校验。
- 不得用浮点算地板或金额。
- 不得让公告发布去改模型 `listing_state`。
- 不得删除被引用的渠道（必须 409 并给路径）。
- 不得回吐上游 Key 明文。

## 8. 完成回执

```
卡号: T15
接口清单: 路径 / 方法 / 权限点 / 对应 F 编号
F 覆盖: F14-1..6、F16-1..7、F17-1..3 → 用例名
地板校验: 公式代码片段 + A11 输出
公示期: A10 输出
来源地: A2 输出
上游 Key: A4 的 SQL 证据
配置白名单: A15 输出
遗留:
```

---

## 9. 第一段交付记录（2026-10-27）

**范围**：交付物 1–7（渠道 + 模型主数据）+ 技术债清偿（状态类错误码收敛、权限矩阵扩容）。定价/分组/公告/风控/用户/配置留第二段。

### 步骤 A · 状态类错误码收敛（技术债清偿）

上一轮立的规矩：**「用户照 hint 改请求体能否成功」——改得成功是 body 问题，改不成功是状态/配额问题，必须有自己的码**。T12 已建 `key_limit_reached`，但 T13 为控制改动面把 12 处状态类拒绝暂堆在 `invalid_request_body` 上。

本轮新增 4 码（均 409 / `conflict_error` / `BUSINESS` / 不可重试），总数 **70 → 74**：

| 码 | 覆盖场景 |
| :--- | :--- |
| `resource_state_conflict` | 退款单/订单/提额单状态迁移不合法（订单非 `PAID`、已有进行中退款、CAS 失败、套餐无日额度可提额等） |
| `trial_already_claimed` | 体验版防刷命中（设备指纹 / 手机号 / IP 段 / 账号已领），四因子共用一条 message，**不披露命中项** |
| `invoice_already_issued` | 同订单 `ISSUED` 后再申请 |
| `price_conflict` | 价格唯一真相源冲突（第二段使用，本段建档） |

12 处调用点逐处判定：**10 处改专用码，2 处保留 `invalid_request_body`**——`invoice.go:294`（改小 `amount_fen` 即可成功）与 `quota_apply.go:136`（改小目标额度即可成功）确属 body 类。

### 步骤 B · 权限矩阵

权限点扩到 **19 个**：`user.read/write`、`key.read/write`、`model.read/write`、`price.write`、`group.write`、`channel.write`、`notice.write`、`risk.write`、`config.write`、`fund.read/write/approve`、`ticket.reply`、`audit.read`、`admin.read`、`admin.all`。

| 角色 | 关键权限 |
| :--- | :--- |
| `PERSONAL` | `user.read`、`key.*`、`model.read`、`fund.read/write`（仅自己的数据） |
| `OPS` | 运营全集，**不含** `fund.approve` |
| `FINANCE` | `fund.read`、`fund.approve`、`audit.read`、`model.read`、`admin.read`，**不含** `price.write`/`channel.write` |
| `SUPER_ADMIN` | 全部 + `admin.all` |

**`admin.read` 是本次新增**（E-31）：后台读接口若复用 `model.read`，`PERSONAL` 就能读到 `cost_*` 成本价与渠道 `base_url`，违反 B-48。已实测 13 条 `/api/admin/*` 路由对 `PERSONAL` 全 403 `permission_denied`（`server_authz_test.go`，13 个子测试）。

**「10 个角色」是文档笔误**（E-30）：PRD §6.3、TDS 1209 行、openapi `UserRole` 均只有 4 个角色。按 4 个实现，未编造角色名。

### 步骤 C · 渠道（交付物 1–4）

6 条路由（读 `admin.read`，写 `channel.write`）：`GET /api/admin/channels`、`GET/PATCH/DELETE /api/admin/channels/{id}`、`POST /api/admin/channels`、`POST /api/admin/channels/{id}/disable`。

四条硬约束实测：

| 约束 | 证据 |
| :--- | :--- |
| 来源地拒绝 | `region=OVERSEAS` → 400「V1.0 仅支持境内渠道」，DB 计数 0 |
| 删除查引用 | 被 `t_route` 引用 → 409 `channel_in_use` + hint；解引用后 200；软删后详情 404 `channel_not_found`（不 500） |
| 上游 Key 密文 | `HEX(LEFT(upstream_key_enc,3))=76313A`（`v1:` 前缀的信封本体，**未二次 base64**，E-17）；`upstream_key_mask=sk-****1234`；`Box.Open` 回原文；响应结构体 `channelView` **无** `upstream_key` 字段，明文只存在于入站请求 DTO |
| 权重即时生效 | 改权重后 `PublishRoutes` 调用 1 次；审计 before/after 含 `weight` 100→10 |

专属名单表 `t_channel_dedicated_user`（0017）已存在，切回 `ALL` 时清空。

### 步骤 D · 模型主数据（交付物 5–7）

`GET /api/admin/models`、`GET/POST/PATCH /api/admin/models/{id}`、`POST .../listing-state`、`POST .../recommend-weight`。

- `LISTED → CATALOG_ONLY`：不需公示期，权重**自动清零**并留痕，生成「成本调整」公告草稿（`t_announcement` 仅 INSERT `DRAFT`）
- `CATALOG_ONLY → LISTED`：生成「上新」草稿
- `CATALOG_ONLY` 设非 0 权重 → 409 `resource_state_conflict`
- 模型 `region=OVERSEAS` → 400 拒绝
- 单价/缓存倍率/阶梯本段**只读**（属第二段定价域）

### 遗留

1. **模型详情 404 语义**：现有码表只有数据面 400 的 `model_not_found`，后台详情复用它返回 400。若要求 404，需再加码或复用一个 404 通用码，留第二段裁定。
2. 专属用户 ID 未校验存在性（表无外键），仅校验 `> 0` 与去重。
3. `price_conflict` 已建档但实际使用在第二段（定价唯一真相源）。
4. 未新增迁移（`0022`/`0023` 之外无占用）、未新增配置键。
5. 本机无 gcc，`-race` 未跑；`gofmt` 未重排既有 CRLF 文件。
