# T12 · 用户 / 密钥 / 模型域 API

| 项 | 值 |
| :--- | :--- |
| 卡片编号 | T12 |
| 阶段 | 4 · 控制面 |
| 优先级 | P0 |
| 依赖 | T11（+ T03 表结构、T06 `KeyAuthWriter`） |
| 可并行 | 与 T13 / T14 / T15 并行 |
| 交付根 | `internal/control/user/`、`internal/control/key/`、`internal/control/model/` |
| 状态 | ⬜ 未开始 |

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
